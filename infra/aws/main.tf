terraform {
  required_version = ">= 1.6.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}

variable "region" {
  type = string
}
variable "domain" {
  type        = string
  default     = ""
  description = "Public hostname whose A record will point to the coordinator's Elastic IP."
  validation {
    condition     = var.domain == "" || (can(regex("^[a-z0-9][a-z0-9.-]+[a-z0-9]$", var.domain)) && strcontains(var.domain, ".") && !strcontains(var.domain, ".."))
    error_message = "Use a lowercase DNS hostname, or leave empty for SSM access."
  }
}
variable "access_mode" {
  type        = string
  default     = "public"
  description = "public uses direct HTTPS; cloudfront uses a private VPC origin; ssm keeps both interfaces private."
  validation {
    condition     = contains(["public", "cloudfront", "ssm"], var.access_mode)
    error_message = "Use public, cloudfront or ssm."
  }
}
variable "cloudfront_origin_security_group" {
  type        = string
  default     = ""
  description = "AWS-managed CloudFront VPC-origin security group; required for cloudfront mode."
  validation {
    condition     = var.cloudfront_origin_security_group == "" || can(regex("^sg-[a-f0-9]+$", var.cloudfront_origin_security_group))
    error_message = "Use an AWS security group ID."
  }
}
variable "operator_origin" {
  type    = string
  default = ""
  validation {
    condition     = var.operator_origin == "" || can(regex("^https://[a-z0-9]+\\.execute-api\\.[a-z0-9-]+\\.amazonaws\\.com$", var.operator_origin))
    error_message = "Use the exact HTTPS operator API Gateway origin."
  }
}
variable "github_oauth_secret_arn" {
  type        = string
  default     = ""
  description = "Optional dedicated GitHub OAuth App credentials in Secrets Manager. Never pass secret values through Terraform."
  validation {
    condition     = var.github_oauth_secret_arn == "" || can(regex("^arn:aws:secretsmanager:[a-z0-9-]+:[0-9]{12}:secret:[A-Za-z0-9/_+=.@-]+$", var.github_oauth_secret_arn))
    error_message = "Use an exact Secrets Manager ARN, or leave empty to disable GitHub linking."
  }
}
variable "finalize_workers" {
  description = "Parallel core finalization circuits; reserve four vCPUs and four GiB RAM per worker, with a 12 GiB minimum memory budget."
  type        = number
  default     = 1
  validation {
    condition     = var.finalize_workers >= 1 && var.finalize_workers <= 41 && floor(var.finalize_workers) == var.finalize_workers
    error_message = "Choose an integer from 1 to 41 and size the instance for its CPU and memory budget."
  }
}
variable "operator_role_arn" {
  type    = string
  default = ""
  validation {
    condition     = var.operator_role_arn == "" || can(regex("^arn:aws:iam::[0-9]{12}:role/[A-Za-z0-9_+=,.@/-]+$", var.operator_role_arn))
    error_message = "Use an exact IAM role ARN."
  }
}
variable "operator_gateway_security_group" {
  type    = string
  default = ""
  validation {
    condition     = var.operator_gateway_security_group == "" || can(regex("^sg-[a-f0-9]+$", var.operator_gateway_security_group))
    error_message = "Use the private operator Lambda security group ID."
  }
}
variable "name" {
  type    = string
  default = "spr-ceremony"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,29}$", var.name))
    error_message = "Use a lowercase deployment name, 3–30 letters, digits or hyphens."
  }
}
variable "instance_type" {
  type    = string
  default = "m7i.xlarge"
  validation {
    condition     = contains(["m7i.xlarge", "m7i.2xlarge", "c7i.24xlarge", "c7i.48xlarge"], var.instance_type)
    error_message = "Use a supported x86 coordinator or temporary c7i audit host."
  }
}
variable "data_gib" {
  type    = number
  default = 200
  validation {
    condition     = var.data_gib >= 100 && var.data_gib <= 2048
    error_message = "Use 100–2048 GiB for retained ceremony transcripts."
  }
}
data "aws_ec2_instance_type" "coordinator" {
  instance_type = var.instance_type
}
variable "alarm_email" {
  type    = string
  default = ""
}

provider "aws" {
  region = var.region
  default_tags { tags = { Application = "Solana Privacy Rings", Deployment = var.name, ManagedBy = "Terraform" } }
}
data "aws_caller_identity" "current" {}
data "aws_availability_zones" "available" { state = "available" }
data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]
  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

resource "aws_vpc" "ceremony" {
  cidr_block           = "10.83.0.0/24"
  enable_dns_hostnames = true
  enable_dns_support   = true
}
resource "aws_subnet" "ceremony" {
  vpc_id            = aws_vpc.ceremony.id
  cidr_block        = "10.83.0.0/26"
  availability_zone = data.aws_availability_zones.available.names[0]
}
resource "aws_internet_gateway" "ceremony" { vpc_id = aws_vpc.ceremony.id }
resource "aws_route_table" "ceremony" {
  vpc_id = aws_vpc.ceremony.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.ceremony.id
  }
}
resource "aws_route_table_association" "ceremony" {
  subnet_id      = aws_subnet.ceremony.id
  route_table_id = aws_route_table.ceremony.id
}
resource "aws_security_group" "ceremony" {
  name_prefix = "${var.name}-"
  vpc_id      = aws_vpc.ceremony.id
  description = "Ceremony access. Internal mode has no ingress; administration uses outbound SSM."
  dynamic "ingress" {
    for_each = var.access_mode == "public" ? [80, 443] : []
    content {
      from_port   = ingress.value
      to_port     = ingress.value
      protocol    = "tcp"
      cidr_blocks = ["0.0.0.0/0"]
    }
  }
  dynamic "ingress" {
    for_each = var.access_mode == "cloudfront" ? [var.cloudfront_origin_security_group] : []
    content {
      from_port       = 8082
      to_port         = 8082
      protocol        = "tcp"
      security_groups = [ingress.value]
      description     = "Private CloudFront VPC origin only"
    }
  }
  dynamic "ingress" {
    for_each = var.operator_gateway_security_group != "" ? [var.operator_gateway_security_group] : []
    content {
      from_port       = 8083
      to_port         = 8083
      protocol        = "tcp"
      security_groups = [ingress.value]
      description     = "Authenticated operator Lambda only"
    }
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

module "archive" {
  source          = "../s3"
  region          = var.region
  bucket_name     = "${var.name}-${data.aws_caller_identity.current.account_id}-artifacts"
  prefix          = "core/v1"
  browser_origins = [local.origin]
}
locals {
  bucket      = "${var.name}-${data.aws_caller_identity.current.account_id}-artifacts"
  origin      = var.access_mode == "ssm" ? "http://127.0.0.1:8080" : "https://${var.domain}"
  origin_host = var.access_mode == "ssm" ? "127.0.0.1:8080" : var.domain
}
resource "aws_iam_role" "coordinator" {
  name_prefix        = "${var.name}-"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole" }] })
}
resource "aws_iam_role_policy_attachment" "archive" {
  role       = aws_iam_role.coordinator.name
  policy_arn = module.archive.coordinator_policy_arn
}
resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.coordinator.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}
# Only metadata enters Terraform state. Key versions are installed separately.
resource "aws_secretsmanager_secret" "coordinator_key" {
  name                    = "${var.name}/coordinator-signing-key"
  description             = "Private OpenPGP coordinator keys, versioned by ceremony manifest digest"
  recovery_window_in_days = 30
  lifecycle { prevent_destroy = true }
}
resource "aws_iam_role_policy" "runtime" {
  role = aws_iam_role.coordinator.id
  policy = jsonencode({ Version = "2012-10-17", Statement = concat([
    { Effect = "Allow", Action = "s3:GetObject", Resource = "arn:aws:s3:::${local.bucket}/deployment/*" },
    { Effect = "Allow", Action = "secretsmanager:GetSecretValue", Resource = aws_secretsmanager_secret.coordinator_key.arn },
    { Effect = "Allow", Action = "cloudwatch:PutMetricData", Resource = "*", Condition = { StringEquals = { "cloudwatch:namespace" = "SPR/Ceremony" } } }
  ], var.github_oauth_secret_arn == "" ? [] : [{ Effect = "Allow", Action = "secretsmanager:GetSecretValue", Resource = var.github_oauth_secret_arn }]) })
}
resource "aws_iam_instance_profile" "coordinator" { role = aws_iam_role.coordinator.name }
resource "aws_ebs_volume" "data" {
  availability_zone = aws_subnet.ceremony.availability_zone
  size              = var.data_gib
  type              = "gp3"
  encrypted         = true
  tags              = { SPRBackup = var.name }
  lifecycle { prevent_destroy = true }
}
resource "aws_instance" "coordinator" {
  ami                         = data.aws_ami.ubuntu.id
  instance_type               = var.instance_type
  subnet_id                   = aws_subnet.ceremony.id
  vpc_security_group_ids      = [aws_security_group.ceremony.id]
  associate_public_ip_address = true
  iam_instance_profile        = aws_iam_instance_profile.coordinator.name
  disable_api_termination     = true
  metadata_options {
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
    instance_metadata_tags      = "disabled"
  }
  root_block_device {
    volume_size = 32
    volume_type = "gp3"
    encrypted   = true
  }
  user_data = templatefile("${path.module}/bootstrap.sh.tftpl", {
    domain          = var.domain, region = var.region, name = var.name,
    public_https    = var.access_mode == "public", cloudfront = var.access_mode == "cloudfront", origin = local.origin, origin_host = local.origin_host,
    volume_id       = replace(aws_ebs_volume.data.id, "-", ""),
    s3_config       = module.archive.s3_config,
    signing_secret  = aws_secretsmanager_secret.coordinator_key.arn,
    key_loader      = file("${path.module}/load-signing-key.py")
    github_secret   = var.github_oauth_secret_arn
    github_loader   = file("${path.module}/load-github-oauth.py")
    operator_origin = var.operator_origin, operator_role = var.operator_role_arn, finalize_workers = var.finalize_workers
  })
  user_data_replace_on_change = false
  tags                        = { Name = var.name }
  lifecycle {
    ignore_changes = [ami, user_data]
    precondition {
      condition = var.operator_origin == "" ? true : (
        data.aws_ec2_instance_type.coordinator.default_vcpus >= max(4, var.finalize_workers * 4) &&
        data.aws_ec2_instance_type.coordinator.memory_size >= (max(12, var.finalize_workers * 4) + 4) * 1024
      )
      error_message = "Instance is too small for finalize_workers: reserve four vCPUs and four GiB per worker, at least 12 GiB for finalization, plus four GiB for services."
    }
    precondition {
      condition     = var.access_mode == "ssm" || var.domain != ""
      error_message = "Public access requires a hostname. Use access_mode = ssm for an internal deployment."
    }
    precondition {
      condition     = var.access_mode != "cloudfront" || var.cloudfront_origin_security_group != ""
      error_message = "CloudFront access requires its AWS-managed VPC-origin security group."
    }
    precondition {
      condition     = var.operator_origin == "" || (var.access_mode == "cloudfront" && var.operator_role_arn != "" && var.operator_gateway_security_group != "")
      error_message = "The operator gateway requires CloudFront mode, its exact IAM role and its private Lambda security group."
    }
  }
  depends_on = [aws_route_table_association.ceremony, aws_iam_role_policy_attachment.ssm, aws_iam_role_policy_attachment.archive]
}
resource "aws_volume_attachment" "data" {
  device_name                    = "/dev/sdf"
  volume_id                      = aws_ebs_volume.data.id
  instance_id                    = aws_instance.coordinator.id
  stop_instance_before_detaching = true
}
resource "aws_eip" "ceremony" {
  count  = var.access_mode == "public" ? 1 : 0
  domain = "vpc"
}
resource "aws_eip_association" "ceremony" {
  count         = var.access_mode == "public" ? 1 : 0
  allocation_id = aws_eip.ceremony[0].id
  instance_id   = aws_instance.coordinator.id
}
moved {
  from = aws_eip.ceremony
  to   = aws_eip.ceremony[0]
}
moved {
  from = aws_eip_association.ceremony
  to   = aws_eip_association.ceremony[0]
}

resource "aws_iam_role" "backup" {
  name_prefix        = "${var.name}-backup-"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "dlm.amazonaws.com" }, Action = "sts:AssumeRole" }] })
}
resource "aws_iam_role_policy_attachment" "backup" {
  role       = aws_iam_role.backup.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSDataLifecycleManagerServiceRole"
}
resource "aws_dlm_lifecycle_policy" "backup" {
  description        = "Daily encrypted coordinator snapshots"
  execution_role_arn = aws_iam_role.backup.arn
  state              = "ENABLED"
  depends_on         = [aws_iam_role_policy_attachment.backup]
  policy_details {
    resource_types = ["VOLUME"]
    target_tags    = { SPRBackup = var.name }
    schedule {
      name = "daily"
      create_rule {
        interval      = 24
        interval_unit = "HOURS"
        times         = ["03:00"]
      }
      retain_rule { count = 7 }
      copy_tags = true
    }
  }
}
resource "aws_sns_topic" "alarms" { name_prefix = "${var.name}-alarms-" }
resource "aws_sns_topic_subscription" "email" {
  count     = var.alarm_email == "" ? 0 : 1
  topic_arn = aws_sns_topic.alarms.arn
  protocol  = "email"
  endpoint  = var.alarm_email
}
resource "aws_cloudwatch_metric_alarm" "disk" {
  alarm_name          = "${var.name}-disk"
  namespace           = "SPR/Ceremony"
  metric_name         = "DiskUsedPercent"
  dimensions          = { Deployment = var.name }
  comparison_operator = "GreaterThanThreshold"
  threshold           = 80
  period              = 300
  evaluation_periods  = 2
  statistic           = "Maximum"
  treat_missing_data  = "breaching"
  alarm_actions       = [aws_sns_topic.alarms.arn]
}
resource "aws_cloudwatch_metric_alarm" "health" {
  alarm_name          = "${var.name}-health"
  namespace           = "SPR/Ceremony"
  metric_name         = "Healthy"
  dimensions          = { Deployment = var.name }
  comparison_operator = "LessThanThreshold"
  threshold           = 1
  period              = 300
  evaluation_periods  = 2
  statistic           = "Minimum"
  treat_missing_data  = "breaching"
  alarm_actions       = [aws_sns_topic.alarms.arn]
}
resource "aws_ssm_document" "panel" {
  name          = "${var.name}-control-panel"
  document_type = "Session"
  content       = jsonencode({ schemaVersion = "1.0", description = "Private ceremony control panel", sessionType = "Port", properties = { type = "LocalPortForwarding", portNumber = "8081", localPortNumber = "8081" } })
}
resource "aws_ssm_document" "participant" {
  count         = var.access_mode == "ssm" ? 1 : 0
  name          = "${var.name}-participant"
  document_type = "Session"
  content       = jsonencode({ schemaVersion = "1.0", description = "Internal ceremony participant site", sessionType = "Port", properties = { type = "LocalPortForwarding", portNumber = "8080", localPortNumber = "8080" } })
}

output "instance_id" { value = aws_instance.coordinator.id }
output "public_ip" { value = var.access_mode == "public" ? aws_eip.ceremony[0].public_ip : null }
output "url" { value = local.origin }
output "access_mode" { value = var.access_mode }
output "bucket" { value = local.bucket }
output "s3_config" { value = module.archive.s3_config }
output "panel_document" { value = aws_ssm_document.panel.name }
output "participant_document" { value = var.access_mode == "ssm" ? aws_ssm_document.participant[0].name : null }
output "alarm_topic" { value = aws_sns_topic.alarms.arn }
output "data_volume" { value = aws_ebs_volume.data.id }

output "signing_key_secret_arn" { value = aws_secretsmanager_secret.coordinator_key.arn }
output "github_oauth_secret_arn" { value = var.github_oauth_secret_arn }
output "operator_origin" { value = var.operator_origin }
output "operator_role_arn" { value = var.operator_role_arn }

output "finalize_workers" { value = var.finalize_workers }
