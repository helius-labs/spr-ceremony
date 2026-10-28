terraform {
  required_version = ">= 1.6.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}

variable "region" { type = string }
variable "instance_id" { type = string }
variable "name" { type = string }

provider "aws" {
  region = var.region
  default_tags { tags = { Application = "Solana Privacy Rings", Deployment = var.name, ManagedBy = "Terraform" } }
}

data "aws_instance" "coordinator" { instance_id = var.instance_id }
data "aws_subnet" "coordinator" { id = data.aws_instance.coordinator.subnet_id }

resource "aws_cloudfront_vpc_origin" "coordinator" {
  vpc_origin_endpoint_config {
    name                   = var.name
    arn                    = data.aws_instance.coordinator.arn
    http_port              = 8082
    https_port             = 443
    origin_protocol_policy = "http-only"
    origin_ssl_protocols {
      items    = ["TLSv1.2"]
      quantity = 1
    }
  }
}

data "aws_security_group" "cloudfront" {
  filter {
    name   = "vpc-id"
    values = [data.aws_subnet.coordinator.vpc_id]
  }
  filter {
    name   = "group-name"
    values = ["CloudFront-VPCOrigins-Service-SG*"]
  }
  depends_on = [aws_cloudfront_vpc_origin.coordinator]
}

data "aws_cloudfront_cache_policy" "disabled" { name = "Managed-CachingDisabled" }
data "aws_cloudfront_origin_request_policy" "viewer" { name = "Managed-AllViewer" }

resource "aws_cloudfront_distribution" "participants" {
  enabled             = true
  is_ipv6_enabled     = true
  comment             = "Solana Privacy Rings public rehearsal; private VPC origin"
  price_class         = "PriceClass_100"
  http_version        = "http2and3"
  wait_for_deployment = true

  origin {
    domain_name = data.aws_instance.coordinator.private_dns
    origin_id   = "ceremony"
    vpc_origin_config {
      vpc_origin_id            = aws_cloudfront_vpc_origin.coordinator.id
      origin_keepalive_timeout = 5
      origin_read_timeout      = 60
    }
  }
  default_cache_behavior {
    target_origin_id         = "ceremony"
    viewer_protocol_policy   = "https-only"
    allowed_methods          = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
    cached_methods           = ["GET", "HEAD"]
    cache_policy_id          = data.aws_cloudfront_cache_policy.disabled.id
    origin_request_policy_id = data.aws_cloudfront_origin_request_policy.viewer.id
    compress                 = false
  }
  restrictions {
    geo_restriction { restriction_type = "none" }
  }
  viewer_certificate {
    cloudfront_default_certificate = true
    # CloudFront fixes its default certificate policy to TLSv1. Native downloads
    # require TLS 1.2; use an ACM certificate and custom domain for production.
    minimum_protocol_version = "TLSv1"
  }
  # Never retain an invitation error after a contributor receives a new link.
  dynamic "custom_error_response" {
    for_each = [400, 403, 404, 405, 414, 416, 500, 501, 502, 503, 504]
    content {
      error_code            = custom_error_response.value
      error_caching_min_ttl = 0
    }
  }
}

output "url" { value = "https://${aws_cloudfront_distribution.participants.domain_name}" }
output "domain" { value = aws_cloudfront_distribution.participants.domain_name }
output "distribution_id" { value = aws_cloudfront_distribution.participants.id }
output "vpc_origin_id" { value = aws_cloudfront_vpc_origin.coordinator.id }
output "origin_security_group" { value = data.aws_security_group.cloudfront.id }
