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
  description = "public enables HTTPS ingress; ssm exposes both interfaces only through authenticated port forwarding."
  validation {
    condition     = contains(["public", "ssm"], var.access_mode)
    error_message = "Use public or ssm."
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
    condition     = contains(["m7i.xlarge", "m7i.2xlarge"], var.instance_type)
    error_message = "Use a supported x86 coordinator with at least 16 GiB RAM."
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
resource "aws_iam_role_policy" "runtime" {
  role = aws_iam_role.coordinator.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [
    { Effect = "Allow", Action = "s3:GetObject", Resource = "arn:aws:s3:::${local.bucket}/deployment/*" },
    { Effect = "Allow", Action = "cloudwatch:PutMetricData", Resource = "*", Condition = { StringEquals = { "cloudwatch:namespace" = "SPR/Ceremony" } } }
  ] })
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
    domain       = var.domain, region = var.region, name = var.name,
    public_https = var.access_mode == "public", origin = local.origin, origin_host = local.origin_host,
    volume_id    = replace(aws_ebs_volume.data.id, "-", ""),
    s3_config    = module.archive.s3_config
  })
  user_data_replace_on_change = false
  tags                        = { Name = var.name }
  lifecycle {
    ignore_changes = [ami, user_data]
    precondition {
      condition     = var.access_mode == "ssm" || var.domain != ""
      error_message = "Public access requires a hostname. Use access_mode = ssm for an internal deployment."
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
  content       = jsonencode({ schemaVersion = "1.0", description = "Private ceremony control panel", sessionType = "Port", properties = { portNumber = "8081", localPortNumber = "8081" } })
}
resource "aws_ssm_document" "participant" {
  count         = var.access_mode == "ssm" ? 1 : 0
  name          = "${var.name}-participant"
  document_type = "Session"
  content       = jsonencode({ schemaVersion = "1.0", description = "Internal ceremony participant site", sessionType = "Port", properties = { portNumber = "8080", localPortNumber = "8080" } })
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
