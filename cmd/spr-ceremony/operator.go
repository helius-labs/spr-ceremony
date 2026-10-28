package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	ceremony "github.com/helius-labs/spr-ceremony"
)

func runOperator(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spr-ceremony operator <connect|invite NAME|reissue NAME_OR_ID|revoke NAME_OR_ID|status|audit> [flags]")
	}
	command, remaining, target := args[0], args[1:], ""
	if command == "invite" || command == "reissue" || command == "revoke" {
		if len(remaining) == 0 || strings.HasPrefix(remaining[0], "-") {
			return fmt.Errorf("operator %s requires a name or invitation ID before flags", command)
		}
		target, remaining = remaining[0], remaining[1:]
	}
	f := flag.NewFlagSet("operator "+command, flag.ContinueOnError)
	profile := f.String("config", "ceremony-operator.json", "saved operator connection settings; contains no credentials")
	var publicURL, controlURL, out, work *string
	var hours *int
	var maxDomain *uint64
	var asJSON *bool
	switch command {
	case "connect":
		publicURL = f.String("url", "http://127.0.0.1:8080", "participant origin")
		controlURL = f.String("control-url", "http://127.0.0.1:8081", "numeric loopback control origin through SSM")
	case "invite", "reissue":
		out = f.String("out", "", "new private invitation file; defaults to a unique file in invitations/")
		hours = f.Int("hours", 24, "invitation lifetime, 1–168 hours")
	case "revoke":
	case "status":
		asJSON = f.Bool("json", false, "public status as JSON")
	case "audit":
		work = f.String("work", "audit", "reusable public audit workspace")
		maxDomain = f.Uint64("max-domain", 1<<22, "maximum in-memory SRS domain")
	default:
		return fmt.Errorf("unknown operator command; use connect, invite, reissue, revoke, status or audit")
	}
	if err := f.Parse(remaining); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected operator argument")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first interrupt cancels transfers. A second retains the normal process
	// termination behavior if native cryptographic verification is still busy.
	go func() { <-ctx.Done(); stop() }()
	if command == "connect" {
		file, err := newPrivateOutput(*profile, "")
		if err != nil {
			return err
		}
		defer file.cleanup()
		config, err := ceremony.ConnectOperator(ctx, *publicURL, *controlURL)
		if err != nil {
			return err
		}
		if err = file.write(config); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Connected. Settings:", file.path)
		fmt.Fprintln(os.Stdout, "Manifest SHA256:", config.Manifest.SHA256)
		return nil
	}
	var config ceremony.OperatorConfig
	if err := ceremony.ReadJSON(*profile, &config); err != nil {
		return fmt.Errorf("cannot read operator settings; run operator connect first")
	}
	operator, err := ceremony.NewOperator(config)
	if err != nil {
		return err
	}
	switch command {
	case "status":
		status, err := operator.Status(ctx)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(status)
		}
		marker := ""
		if status.InsecureTestSetup {
			marker = " · test ceremony"
		}
		fmt.Fprintf(os.Stdout, "%s%s · %d active reservations\n", status.Phase, marker, len(status.Reservations))
		for _, invitation := range status.Invitations {
			state := "active"
			if !invitation.HasLink {
				state = "revoked"
			} else if !time.Now().Before(invitation.Expires) {
				state = "expired"
			}
			fmt.Fprintf(os.Stdout, "%s  %d/%d accepted  %s  %s\n", invitation.Contributor, invitation.Accepted, invitation.Total, state, invitation.ID[:12])
		}
		fmt.Fprintln(os.Stdout, "Checkpoint:", status.Checkpoint.SHA256)
		return nil
	case "invite", "reissue":
		id := ""
		if command == "reissue" {
			id, err = operatorIdentity(ctx, operator, target)
			if err != nil {
				return err
			}
		}
		// Reserve a mode-0600 file before issuing the private grant. Never print a
		// bearer URL or embed its secret in a command, process argument or history.
		file, err := newPrivateOutput(*out, "invitation-")
		if err != nil {
			return err
		}
		defer file.cleanup()
		var invitation ceremony.Invitation
		if command == "invite" {
			invitation, err = operator.Invite(ctx, target, *hours)
		} else {
			invitation, err = operator.Reissue(ctx, id, *hours)
		}
		if err != nil {
			return err
		}
		if err = file.write(invitation); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Private invitation for %s: %s\n", invitation.Contributor, file.path)
		fmt.Fprintln(os.Stdout, "Send that file privately with this command:")
		fmt.Fprintln(os.Stdout, "./spr-ceremony contribute --invitation "+shellArgument(filepath.Base(file.path)))
		return nil
	case "revoke":
		id, err := operatorIdentity(ctx, operator, target)
		if err != nil {
			return err
		}
		if err = operator.Revoke(ctx, id); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Access revoked. Accepted contributions are kept.")
		return nil
	case "audit":
		point, directory, err := operator.Audit(ctx, *work, *maxDomain, os.Stderr)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Transcript verified: core")
		fmt.Fprintln(os.Stdout, "Manifest:", point.Manifest.SHA256)
		fmt.Fprintln(os.Stdout, "Checkpoint:", point.Checkpoint.SHA256)
		fmt.Fprintln(os.Stdout, "Public snapshot:", directory)
		return nil
	}
	return fmt.Errorf("unknown operator command")
}

func operatorIdentity(ctx context.Context, operator *ceremony.Operator, target string) (string, error) {
	if (ceremony.Artifact{SHA256: target, Size: 1}).Validate() == nil && target == strings.ToLower(target) {
		return target, nil
	}
	status, err := operator.Status(ctx)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, invitation := range status.Invitations {
		if invitation.ID == target {
			return invitation.ID, nil
		}
		if invitation.Contributor == target || len(target) >= 12 && strings.HasPrefix(invitation.ID, target) {
			matches = append(matches, invitation.ID)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("invitation is missing or ambiguous; use its ID from operator status")
	}
	return matches[0], nil
}

func shellArgument(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

type privateOutput struct {
	path     string
	file     *os.File
	complete bool
}

func newPrivateOutput(path, prefix string) (*privateOutput, error) {
	if path == "" {
		if err := os.MkdirAll("invitations", 0700); err != nil {
			return nil, err
		}
		file, err := os.CreateTemp("invitations", prefix+"*.json")
		if err != nil {
			return nil, err
		}
		return &privateOutput{path: file.Name(), file: file}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("output must be a new writable file: %w", err)
	}
	return &privateOutput{path: path, file: file}, nil
}
func (p *privateOutput) write(value any) error {
	encoder := json.NewEncoder(p.file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := p.file.Sync(); err != nil {
		return err
	}
	if err := p.file.Close(); err != nil {
		return err
	}
	p.complete = true
	return nil
}
func (p *privateOutput) cleanup() {
	_ = p.file.Close()
	if !p.complete {
		_ = os.Remove(p.path)
	}
}
