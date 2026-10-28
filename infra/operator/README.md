# AWS operator gateway

HTTPS operator commands use API Gateway IAM authentication and an exact allowed
SSO role. A Lambda inside the coordinator VPC forwards only supported routes to
private port 8083. It drops caller identity headers and AWS credentials, then
adds API Gateway's verified IAM principal. No secrets enter Terraform state.

Apply after `infra/aws` has created the instance:

```sh
terraform init
terraform plan -var=region=eu-north-1 -var=instance_id=INSTANCE_ID -var=name=spr-ceremony-operator-test -var=operator_role_arn=EXACT_IAM_ROLE_ARN -out=operator.tfplan
terraform apply operator.tfplan
```

Copy `url`, `operator_role_arn` and `gateway_security_group` outputs into the
main module's `operator_origin`, `operator_role_arn` and
`operator_gateway_security_group` variables. Apply the main module, then deploy a
release containing `operator_gateway.go` with `scripts/deploy-aws.py`.
The caller's role needs `execute-api:Invoke` on this API's operator routes.

The CLI saves only public connection settings. Phase operations persist their
status on encrypted coordinator storage and run independently of HTTP requests;
`operator job` resumes observation after disconnecting.

The optional browser panel uses a five-minute pairing challenge approved by
`operator login CODE`. Access requires the browser's separate secret cookie.
Sessions last 30 minutes and use Secure, HttpOnly, SameSite=Strict cookies.
Coordinator restarts invalidate sessions. AWS credentials stay outside the browser.

Security boundary: only this Lambda security group may reach port 8083. Never
publish 8081 or 8083 directly, or trust arbitrary forwarded principal headers.
The Lambda records no request bodies, cookies, signatures or responses. API
access logging is disabled; invocation metadata is retained for seven days.
Keep Terraform state private. See [operator commands](../../docs/operator-cli.md).
