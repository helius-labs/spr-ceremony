package ceremony

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// OperatorConfig contains public connection settings and a manifest trust pin.
// IAM authenticates the gateway or control tunnel; this file contains no credentials.
type OperatorConfig struct {
	Format     string   `json:"format"`
	URL        string   `json:"url"`
	ControlURL string   `json:"control_url,omitempty"`
	GatewayURL string   `json:"operator_url,omitempty"`
	AWSProfile string   `json:"aws_profile,omitempty"`
	Manifest   Artifact `json:"manifest"`
}

type OperatorStatus struct {
	Protocol     int               `json:"operator_protocol"`
	Phase        string            `json:"phase"`
	Beacon       *BeaconPlan       `json:"beacon"`
	Manifest     Artifact          `json:"manifest"`
	Checkpoint   Artifact          `json:"checkpoint"`
	Invitations  []AdminInvitation `json:"invitations"`
	Reservations []struct {
		Circuit string    `json:"circuit"`
		Expires time.Time `json:"expires"`
	} `json:"reservations"`
	InsecureTestSetup bool             `json:"insecure_test_setup"`
	S3                bool             `json:"s3"`
	CoordinatorKey    *Artifact        `json:"coordinator_key"`
	Recoveries        []BeaconRecovery `json:"beacon_recoveries"`
}

type Operator struct {
	Config      OperatorConfig
	client      *Client
	credentials aws.CredentialsProvider
	signing     sync.Mutex
}

func validateControlURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Port() == "" {
		return fmt.Errorf("control URL must be a numeric loopback HTTP origin reached through SSM")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("control URL must be a numeric loopback HTTP origin reached through SSM")
	}
	return nil
}

func NewOperator(config OperatorConfig) (*Operator, error) {
	if config.Format != "spr-operator-v1" || config.Manifest.Validate() != nil || config.Manifest.Size > maxJSONBytes {
		return nil, fmt.Errorf("invalid operator profile; run operator connect first")
	}
	if err := ValidateURL(config.URL); err != nil {
		return nil, err
	}
	if err := validateOperatorConnection(config); err != nil {
		return nil, err
	}
	return &Operator{Config: config, client: NewClient("")}, nil
}

func validateOperatorConnection(config OperatorConfig) error {
	if config.GatewayURL == "" {
		if config.AWSProfile != "" {
			return fmt.Errorf("AWS profile requires an operator endpoint")
		}
		return validateControlURL(config.ControlURL)
	}
	if config.ControlURL != "" {
		return fmt.Errorf("choose one operator connection")
	}
	_, err := operatorRegion(config.GatewayURL)
	return err
}

// operatorRegion restricts AWS signing to an HTTPS API Gateway origin.
func operatorRegion(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" {
		return "", fmt.Errorf("operator endpoint must be an HTTPS API Gateway origin")
	}
	parts := strings.Split(u.Host, ".")
	if len(parts) != 5 || parts[1] != "execute-api" || parts[3] != "amazonaws" || parts[4] != "com" || !safeID.MatchString(parts[0]) || !safeID.MatchString(parts[2]) {
		return "", fmt.Errorf("operator endpoint must be an HTTPS API Gateway origin")
	}
	return parts[2], nil
}

// ConnectOperatorAWS authenticates with the caller's existing AWS SSO profile.
// Only public connection settings are saved; AWS manages its credential cache.
func ConnectOperatorAWS(ctx context.Context, gatewayURL, profile string) (OperatorConfig, error) {
	config := OperatorConfig{Format: "spr-operator-v1", GatewayURL: strings.TrimRight(gatewayURL, "/"), AWSProfile: profile}
	if err := validateOperatorConnection(config); err != nil {
		return config, err
	}
	o := &Operator{Config: config, client: NewClient("")}
	var remote struct {
		URL      string   `json:"url"`
		Manifest Artifact `json:"manifest"`
	}
	if err := o.admin(ctx, "config", nil, &remote); err != nil {
		return config, err
	}
	config.URL, config.Manifest = remote.URL, remote.Manifest
	o, err := NewOperator(config)
	if err != nil {
		return config, err
	}
	if _, err = o.Status(ctx); err != nil {
		return config, err
	}
	_, _, err = o.manifest(ctx)
	return config, err
}

func (o *Operator) sign(ctx context.Context, req *http.Request, body []byte) error {
	o.signing.Lock()
	defer o.signing.Unlock()
	region, err := operatorRegion(o.Config.GatewayURL)
	if err != nil {
		return err
	}
	if o.credentials == nil {
		options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
		if o.Config.AWSProfile != "" {
			options = append(options, awsconfig.WithSharedConfigProfile(o.Config.AWSProfile))
		}
		config, err := awsconfig.LoadDefaultConfig(ctx, options...)
		if err != nil {
			return fmt.Errorf("AWS profile unavailable; check your profile and run aws sso login")
		}
		o.credentials = config.Credentials
	}
	credentials, err := o.credentials.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("AWS login expired or unavailable; run aws sso login for your profile")
	}
	digest := sha256.Sum256(body)
	if err = v4.NewSigner().SignHTTP(ctx, credentials, req, hex.EncodeToString(digest[:]), "execute-api", region, time.Now()); err != nil {
		return fmt.Errorf("could not authenticate the operator request")
	}
	return nil
}

func (o *Operator) ApproveBrowser(ctx context.Context, code string) error {
	if o.Config.GatewayURL == "" {
		return fmt.Errorf("browser login requires an AWS operator endpoint")
	}
	var result struct {
		Approved bool `json:"approved"`
	}
	if err := o.admin(ctx, "login", map[string]string{"code": code}, &result); err != nil {
		return err
	}
	if !result.Approved {
		return fmt.Errorf("browser approval was not confirmed")
	}
	return nil
}

func (o *Operator) StartJob(ctx context.Context, action string, hours int, reason string) (OperatorJob, error) {
	var job OperatorJob
	if o.Config.GatewayURL == "" {
		return job, fmt.Errorf("coordinator operations require the AWS operator endpoint")
	}
	status, err := o.Status(ctx)
	if err != nil {
		return job, err
	}
	id, err := randomBrowserToken()
	if err != nil {
		return job, err
	}
	input := OperatorJobRequest{ID: id, Action: action, Checkpoint: status.Checkpoint.SHA256, Hours: hours, Reason: reason}
	if err = o.admin(ctx, "run", input, &job); err != nil {
		return job, err
	}
	if job.Request != input {
		return job, fmt.Errorf("operation response differs from request")
	}
	return job, nil
}

func (o *Operator) Job(ctx context.Context, id string) (OperatorJob, error) {
	var job OperatorJob
	if o.Config.GatewayURL == "" {
		return job, fmt.Errorf("coordinator operations require the AWS operator endpoint")
	}
	if err := o.admin(ctx, "job", map[string]string{"id": id}, &job); err != nil {
		return job, err
	}
	if !validInvitationID(job.Request.ID) || id != "" && id != "latest" && id != job.Request.ID {
		return job, fmt.Errorf("operation identity mismatch")
	}
	return job, nil
}

// ConnectOperator pins the campaign reported by the authenticated control
// tunnel, and checks the public endpoint against that same manifest.
func ConnectOperator(ctx context.Context, publicURL, controlURL string) (OperatorConfig, error) {
	config := OperatorConfig{Format: "spr-operator-v1", URL: strings.TrimRight(publicURL, "/"), ControlURL: strings.TrimRight(controlURL, "/")}
	if err := ValidateURL(config.URL); err != nil {
		return config, err
	}
	if err := validateControlURL(config.ControlURL); err != nil {
		return config, err
	}
	o := &Operator{Config: config, client: NewClient("")}
	var status OperatorStatus
	if err := o.admin(ctx, "status", nil, &status); err != nil {
		return config, err
	}
	config.Manifest = status.Manifest
	o, err := NewOperator(config)
	if err != nil {
		return config, err
	}
	if err = o.checkStatus(status); err != nil {
		return config, err
	}
	manifest, _, err := o.manifest(ctx)
	if err == nil && manifest.Phase1.InsecureTestSetup != status.InsecureTestSetup {
		err = fmt.Errorf("public and control endpoints disagree about the campaign")
	}
	return config, err
}

func (o *Operator) admin(ctx context.Context, endpoint string, input, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var body io.Reader
	var encoded []byte
	method := http.MethodGet
	if input != nil {
		var err error
		encoded, err = json.Marshal(input)
		if err != nil {
			return err
		}
		body, method = bytes.NewReader(encoded), http.MethodPost
	}
	origin, prefix := o.Config.ControlURL, "/admin/"
	if o.Config.GatewayURL != "" {
		origin, prefix = o.Config.GatewayURL, "/operator/"
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+prefix+endpoint, body)
	if err != nil {
		return fmt.Errorf("invalid control request")
	}
	req.Header.Set("X-SPR-Admin", "1")
	req.Header.Set("Origin", origin)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-SPR-Manifest", o.Config.Manifest.SHA256)
	}
	if o.Config.GatewayURL != "" {
		if err = o.sign(ctx, req, encoded); err != nil {
			return err
		}
	}
	response, err := o.client.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("operator endpoint unavailable: %w", safeRequestError(err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// Never echo an arbitrary server response containing private invitations.
		if response.StatusCode == http.StatusConflict {
			return fmt.Errorf("control request refused; run operator status and use operator reissue ID for an existing invitation")
		}
		return fmt.Errorf("control request rejected (HTTP %d)", response.StatusCode)
	}
	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
	if err != nil || int64(len(responseBytes)) > maxJSONBytes {
		return fmt.Errorf("invalid control response size")
	}
	return decodeRemoteJSON(bytes.NewReader(responseBytes), output)
}

func (o *Operator) checkStatus(status OperatorStatus) error {
	if status.Protocol != 1 {
		return fmt.Errorf("control panel does not support this operator client; update the coordinator")
	}
	if status.Manifest != o.Config.Manifest || status.Checkpoint.Validate() != nil || status.Checkpoint.Size > maxJSONBytes {
		return fmt.Errorf("campaign differs from the pinned operator profile; reconnect explicitly for a new campaign")
	}
	for _, i := range status.Invitations {
		if !validInvitationID(i.ID) || !safeID.MatchString(i.Contributor) || i.Total < 1 || i.Total > 41 || i.Accepted < 0 || i.Accepted > i.Total {
			return fmt.Errorf("invalid invitation status")
		}
	}
	return nil
}

func (o *Operator) Status(ctx context.Context) (OperatorStatus, error) {
	var status OperatorStatus
	if err := o.admin(ctx, "status", nil, &status); err != nil {
		return status, err
	}
	return status, o.checkStatus(status)
}

func (o *Operator) publicObject(ctx context.Context, ref Artifact, limit int64) ([]byte, error) {
	if ref.Validate() != nil || ref.Size > limit {
		return nil, fmt.Errorf("invalid public metadata reference")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(o.Config.URL, "/")+"/v1/objects/"+ref.SHA256, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid public object request")
	}
	response, err := o.client.HTTP.Do(req)
	if err != nil {
		return nil, safeRequestError(err)
	}
	defer response.Body.Close()
	b, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || response.StatusCode != 200 || int64(len(b)) != ref.Size || hashBytes(b) != ref.SHA256 {
		return nil, fmt.Errorf("public metadata does not match its pinned hash and size")
	}
	return b, nil
}

func (o *Operator) manifest(ctx context.Context) (Manifest, []byte, error) {
	var manifest Manifest
	b, err := o.publicObject(ctx, o.Config.Manifest, maxJSONBytes)
	if err != nil {
		return manifest, nil, err
	}
	if err = decodeRemoteJSON(bytes.NewReader(b), &manifest); err != nil {
		return manifest, nil, err
	}
	if manifest.Format != Format || manifest.Engine != Engine || manifest.CoordinatorKey == nil || len(manifest.Circuits) == 0 || len(manifest.Circuits) > 41 {
		return manifest, nil, fmt.Errorf("unsupported or unsigned core campaign")
	}
	if err = validatePhase1Policy(manifest.Phase1); err != nil {
		return manifest, nil, err
	}
	seen := map[string]bool{}
	for _, circuit := range manifest.Circuits {
		if circuit.Spec.Track != Core || !safeID.MatchString(circuit.Spec.ID) || seen[circuit.Spec.ID] {
			return manifest, nil, fmt.Errorf("invalid core circuit set")
		}
		seen[circuit.Spec.ID] = true
		if !manifest.Phase1.InsecureTestSetup && (!IsCatalogSpec(circuit.Spec) || len(manifest.Circuits) != 41) {
			return manifest, nil, fmt.Errorf("production campaign must contain all 41 core circuits")
		}
	}
	return manifest, b, nil
}

func (o *Operator) issue(ctx context.Context, endpoint string, input any, name, id string) (Invitation, error) {
	var result struct {
		Link       string     `json:"link"`
		Invitation Invitation `json:"invitation"`
	}
	if _, err := o.Status(ctx); err != nil {
		return result.Invitation, err
	}
	if err := o.admin(ctx, endpoint, input, &result); err != nil {
		return Invitation{}, err
	}
	i := result.Invitation
	if i.Format != Format || i.Mode != "queue" || i.Track != Core || i.ManifestSHA256 != o.Config.Manifest.SHA256 || strings.TrimRight(i.URL, "/") != strings.TrimRight(o.Config.URL, "/") || !validInvitationID(i.ID) || !validInvitationID(i.Token) || !safeID.MatchString(i.Contributor) || !time.Now().Before(i.Expires) || name != "" && i.Contributor != name || id != "" && i.ID != id {
		return Invitation{}, fmt.Errorf("invitation does not match the requested campaign and identity")
	}
	m, _, err := o.manifest(ctx)
	if err != nil {
		return Invitation{}, err
	}
	if i.InsecureTestSetup != m.Phase1.InsecureTestSetup || len(i.Jobs) != len(m.Circuits) {
		return Invitation{}, fmt.Errorf("invitation scope mismatch")
	}
	for n, circuit := range m.Circuits {
		if !sameCircuit(i.Jobs[n].Circuit, circuit) || i.Jobs[n].Input.Validate() != nil || i.Jobs[n].Sequence < 1 {
			return Invitation{}, fmt.Errorf("invitation circuit mismatch")
		}
	}
	public, err := o.publicObject(ctx, *m.CoordinatorKey, 16384)
	if err != nil {
		return Invitation{}, err
	}
	if err = verifySignedJSON(public, "invitation", i.ManifestSHA256, i); err != nil {
		return Invitation{}, fmt.Errorf("coordinator invitation signature is invalid")
	}
	link, err := InvitationLink(i)
	if err != nil || link != result.Link {
		return Invitation{}, fmt.Errorf("invitation link mismatch")
	}
	return i, nil
}

func (o *Operator) Invite(ctx context.Context, name string, hours int) (Invitation, error) {
	if !safeID.MatchString(name) || hours < 1 || hours > 168 {
		return Invitation{}, fmt.Errorf("use a lowercase pseudonym and 1–168 hours")
	}
	return o.issue(ctx, "invite", map[string]any{"name": name, "hours": hours}, name, "")
}

func (o *Operator) Reissue(ctx context.Context, id string, hours int) (Invitation, error) {
	if !validInvitationID(id) || hours < 1 || hours > 168 {
		return Invitation{}, fmt.Errorf("use an invitation ID and 1–168 hours")
	}
	return o.issue(ctx, "reissue", map[string]any{"id": id, "hours": hours}, "", id)
}

func (o *Operator) Revoke(ctx context.Context, id string) error {
	if !validInvitationID(id) {
		return fmt.Errorf("invalid invitation ID")
	}
	if _, err := o.Status(ctx); err != nil {
		return err
	}
	var response struct {
		Revoked bool `json:"revoked"`
	}
	if err := o.admin(ctx, "revoke", map[string]string{"id": id}, &response); err != nil {
		return err
	}
	if !response.Revoked {
		return fmt.Errorf("revocation was not confirmed")
	}
	return nil
}

// Audit downloads one authenticated checkpoint and independently verifies all
// its public contributions. Public object caching makes interrupted downloads
// reusable; no S3 credentials, invitations or signing keys enter the cache.
func (o *Operator) Audit(ctx context.Context, work string, maxDomain uint64, progress io.Writer) (RecoveryPoint, string, error) {
	if progress == nil {
		progress = io.Discard
	}
	return o.withSnapshot(ctx, work, progress, func(s *Store) error {
		fmt.Fprintf(progress, "Verifying %d circuits, signatures and checkpoint history…\n", len(s.Manifest.Circuits))
		return s.Audit(Core, maxDomain)
	})
}

// Export downloads the finalized public keys and independently reproduces them
// from the pinned transcript. Existing output is verified, never overwritten.
func (o *Operator) Export(ctx context.Context, work, out string, maxDomain uint64, progress io.Writer) (RecoveryPoint, string, error) {
	if progress == nil {
		progress = io.Discard
	}
	if strings.TrimSpace(out) == "" {
		return RecoveryPoint{}, "", fmt.Errorf("choose an export directory")
	}
	return o.withSnapshot(ctx, work, progress, func(s *Store) error {
		life, _, err := s.Lifecycle()
		if err != nil {
			return err
		}
		if life.Phase != "finalized" {
			return fmt.Errorf("finalize the ceremony before exporting keys")
		}
		if _, err = os.Stat(out); os.IsNotExist(err) {
			if err = s.MaterializeExport(out); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if life.FinalExport == nil || Check(filepath.Join(out, "export.json"), *life.FinalExport) != nil {
			return fmt.Errorf("export differs from the finalized checkpoint")
		}
		fmt.Fprintf(progress, "Verifying final keys for %d circuits against the transcript and beacon…\n", len(s.Manifest.Circuits))
		return s.VerifyExport(out, maxDomain)
	})
}

func (o *Operator) withSnapshot(ctx context.Context, work string, progress io.Writer, verify func(*Store) error) (RecoveryPoint, string, error) {
	if progress == nil {
		progress = io.Discard
	}
	var point RecoveryPoint
	status, err := o.Status(ctx)
	if err != nil {
		return point, "", err
	}
	point = RecoveryPoint{Manifest: o.Config.Manifest, Checkpoint: status.Checkpoint}
	if _, _, err = o.manifest(ctx); err != nil {
		return point, "", err
	}
	if err = os.MkdirAll(work, 0700); err != nil {
		return point, "", err
	}
	lock, err := os.OpenFile(filepath.Join(work, ".operator-audit.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return point, "", err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return point, "", fmt.Errorf("another audit is using this workspace")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	cache := filepath.Join(work, "objects")
	if err = os.MkdirAll(cache, 0700); err != nil {
		return point, "", err
	}
	directory := filepath.Join(work, point.Manifest.SHA256, point.Checkpoint.SHA256)
	remote := &operatorPublicStore{client: o.client, origin: strings.TrimRight(o.Config.URL, "/"), cache: cache}
	if _, err = os.Stat(directory); os.IsNotExist(err) {
		fmt.Fprintln(progress, "Downloading the public transcript…")
		if err = Restore(ctx, remote, directory, point.Manifest, point.Checkpoint); err != nil {
			return point, "", err
		}
	} else if err != nil {
		return point, "", err
	}
	s, err := Open(directory)
	if err != nil {
		return point, "", err
	}
	_, checkpoint, err := s.Lifecycle()
	if err != nil || s.ManifestArtifact != point.Manifest || checkpoint != point.Checkpoint {
		return point, "", fmt.Errorf("cached audit snapshot differs from the requested checkpoint")
	}
	if err = verify(s); err != nil {
		return point, "", err
	}
	return point, directory, nil
}

type operatorPublicStore struct {
	client        *Client
	origin, cache string
}

func (*operatorPublicStore) PutObject(context.Context, string, string, Artifact) error {
	return fmt.Errorf("public audit storage is read-only")
}
func (*operatorPublicStore) ReadPointer(context.Context, string) (Artifact, string, error) {
	return Artifact{}, "", fmt.Errorf("audit requires an authenticated checkpoint")
}
func (*operatorPublicStore) ConditionalPut(context.Context, string, Artifact, string) error {
	return fmt.Errorf("public audit storage is read-only")
}
func (*operatorPublicStore) DownloadURL(context.Context, string, time.Duration) (string, error) {
	return "", fmt.Errorf("public audit storage is read-only")
}
func (s *operatorPublicStore) GetObject(ctx context.Context, key, path string, ref Artifact) error {
	if !validInvitationID(ref.SHA256) || ref.Validate() != nil || ref.Size > 5<<30 || key != "objects/"+ref.SHA256 {
		return fmt.Errorf("invalid audit object")
	}
	cached := filepath.Join(s.cache, ref.SHA256)
	if Check(cached, ref) != nil {
		if err := CheckDiskSpace(s.cache, uint64(ref.Size)+(64<<20)); err != nil {
			return err
		}
		if err := s.client.downloadWithURL(ctx, func(ctx context.Context) (string, error) { return s.client.downloadTicket(ctx, s.origin, ref) }, cached, ref); err != nil {
			return err
		}
	}
	if err := os.Link(cached, path); err == nil {
		return nil
	}
	return Atomic(path, func(w io.Writer) error {
		f, err := os.Open(cached)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
}
