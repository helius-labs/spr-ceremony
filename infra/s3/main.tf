terraform {
  required_version = ">= 1.6.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

variable "region" { type = string }
variable "bucket_name" { type = string }
variable "browser_origins" {
  description = "Exact hosted ceremony origins allowed to read presigned public artifacts. Empty uses coordinator downloads."
  type        = list(string)
  default     = []
  validation {
    condition     = alltrue([for origin in var.browser_origins : can(regex("^https://[a-zA-Z0-9.-]+(:[0-9]+)?$", origin))])
    error_message = "Use exact HTTPS origins without paths, wildcards, query strings, or trailing slashes."
  }
}
variable "prefix" {
  type    = string
  default = "core/v1"
  validation {
    condition     = can(regex("^[a-zA-Z0-9][a-zA-Z0-9/_-]*$", var.prefix)) && !endswith(var.prefix, "/")
    error_message = "Use letters, digits, underscores, hyphens and internal slashes; start with a letter or digit."
  }
}

provider "aws" { region = var.region }

resource "aws_s3_bucket" "ceremony" {
  bucket        = var.bucket_name
  force_destroy = false
  tags          = { Purpose = "Zolana core trusted setup" }
  lifecycle { prevent_destroy = true }
}

resource "aws_s3_bucket_versioning" "ceremony" {
  bucket = aws_s3_bucket.ceremony.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_cors_configuration" "browser" {
  count  = length(var.browser_origins) > 0 ? 1 : 0
  bucket = aws_s3_bucket.ceremony.id
  cors_rule {
    allowed_origins = var.browser_origins
    allowed_methods = ["GET", "HEAD"]
    allowed_headers = ["Range"]
    expose_headers  = ["Content-Range", "Content-Length", "ETag"]
    max_age_seconds = 300
  }
}

resource "aws_s3_bucket_public_access_block" "ceremony" {
  bucket                  = aws_s3_bucket.ceremony.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "ceremony" {
  bucket = aws_s3_bucket.ceremony.id
  rule { object_ownership = "BucketOwnerEnforced" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "ceremony" {
  bucket = aws_s3_bucket.ceremony.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}

resource "aws_s3_bucket_policy" "ceremony" {
  bucket = aws_s3_bucket.ceremony.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "RequireTLS"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:*"
        Resource  = [aws_s3_bucket.ceremony.arn, "${aws_s3_bucket.ceremony.arn}/*"]
        Condition = { Bool = { "aws:SecureTransport" = "false", "aws:PrincipalIsAWSService" = "false" } }
      },
      {
        Sid       = "RequireImmutableObjects"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:PutObject"
        Resource  = "${aws_s3_bucket.ceremony.arn}/${var.prefix}/objects/*"
        Condition = {
          Null = { "s3:if-none-match" = "true" }
          Bool = { "s3:ObjectCreationOperation" = "true" }
        }
      },
      {
        Sid       = "RequireConditionalWrites"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:PutObject"
        Resource  = "${aws_s3_bucket.ceremony.arn}/${var.prefix}/*"
        Condition = {
          Null = { "s3:if-match" = "true", "s3:if-none-match" = "true" }
          Bool = { "s3:ObjectCreationOperation" = "true" }
        }
      }
    ]
  })
}

resource "aws_iam_policy" "coordinator" {
  name_prefix = "spr-core-coordinator-"
  description = "Attach to the coordinator workload role; no participant AWS credentials or object deletion."
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject"]
        Resource = "${aws_s3_bucket.ceremony.arn}/${var.prefix}/*"
      },
      {
        Effect = "Allow"
        # HEAD/GET of a missing object returns 403 without ListBucket. The
        # coordinator needs the 404 response to seed immutable objects/pointers.
        Action   = ["s3:ListBucket", "s3:GetBucketVersioning", "s3:GetBucketPublicAccessBlock", "s3:GetEncryptionConfiguration"]
        Resource = aws_s3_bucket.ceremony.arn
      }
    ]
  })
}

output "coordinator_policy_arn" { value = aws_iam_policy.coordinator.arn }
output "s3_config" {
  value = jsonencode({ bucket = aws_s3_bucket.ceremony.id, region = var.region, prefix = var.prefix })
}
