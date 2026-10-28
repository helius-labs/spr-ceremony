# Public rehearsal endpoint

This module adds an HTTPS CloudFront address to an existing test coordinator.
Its VPC origin reaches private port 8082; it opens no public EC2 ports. Caching
is disabled. S3 continues to serve large objects through signed download tickets.

```sh
terraform init
terraform plan -var=region=eu-north-1 -var=instance_id=INSTANCE_ID -var=name=spr-ceremony-public-test -out=public.tfplan
terraform apply public.tfplan
```

In the main `infra/aws` workspace, set `access_mode = "cloudfront"`, `domain`
to this module's `domain`, and `cloudfront_origin_security_group` to its
`origin_security_group`. Add the [operator gateway](../operator/README.md)
outputs for AWS SSO CLI access. Review and apply that plan, then deploy the
verified release. Deployment writes Caddy's private listeners and the new origin
into the existing services; Terraform does not replace the instance.

The AWS-generated hostname needs no DNS setup. CloudFront fixes its default
certificate policy to TLSv1; modern browsers and the installer use TLS 1.2+.
A production hostname with an ACM certificate is needed to enforce a modern
minimum at CloudFront. This module is for marked rehearsals, whose keys must
never be promoted to production.

Preserve all three Terraform states privately. See the
[rehearsal guide](../../docs/rehearsal.md) for contributor and operator commands.
