terraform {
  required_version = ">= 1.6.0"
  required_providers {
    aws     = { source = "hashicorp/aws", version = "~> 6.0" }
    archive = { source = "hashicorp/archive", version = "~> 2.0" }
  }
}

variable "region" { type = string }
variable "instance_id" { type = string }
variable "name" { type = string }
variable "operator_role_arn" {
  type = string
  validation {
    condition     = can(regex("^arn:aws:iam::[0-9]{12}:role/[A-Za-z0-9_+=,.@/-]+$", var.operator_role_arn))
    error_message = "Specify the exact authorized IAM role ARN."
  }
}

provider "aws" {
  region = var.region
  default_tags { tags = { Application = "Solana Privacy Rings", Deployment = var.name, ManagedBy = "Terraform" } }
}
data "aws_instance" "coordinator" { instance_id = var.instance_id }
data "aws_subnet" "coordinator" { id = data.aws_instance.coordinator.subnet_id }
locals {
  principal_prefix = "arn:aws:sts::${split(":", var.operator_role_arn)[4]}:assumed-role/${element(reverse(split("/", var.operator_role_arn)), 0)}/"
}

resource "aws_security_group" "gateway" {
  name_prefix = "${var.name}-gateway-"
  description = "Operator Lambda; private coordinator port only"
  vpc_id      = data.aws_subnet.coordinator.vpc_id
  egress {
    protocol    = "tcp"
    from_port   = 8083
    to_port     = 8083
    cidr_blocks = ["${data.aws_instance.coordinator.private_ip}/32"]
  }
}
resource "aws_iam_role" "gateway" {
  name_prefix        = "${var.name}-gateway-"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "lambda.amazonaws.com" }, Action = "sts:AssumeRole" }] })
}
resource "aws_iam_role_policy_attachment" "vpc" {
  role       = aws_iam_role.gateway.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
}
resource "aws_cloudwatch_log_group" "gateway" {
  name              = "/aws/lambda/${var.name}-gateway"
  retention_in_days = 7
}
data "archive_file" "gateway" {
  type        = "zip"
  source_file = "${path.module}/gateway.py"
  output_path = "${path.module}/gateway.zip"
}
resource "aws_apigatewayv2_api" "operator" {
  name          = var.name
  protocol_type = "HTTP"
}
resource "aws_lambda_function" "gateway" {
  function_name                  = "${var.name}-gateway"
  role                           = aws_iam_role.gateway.arn
  filename                       = data.archive_file.gateway.output_path
  source_code_hash               = data.archive_file.gateway.output_base64sha256
  runtime                        = "python3.13"
  handler                        = "gateway.handler"
  timeout                        = 28
  memory_size                    = 256
  reserved_concurrent_executions = 10
  vpc_config {
    subnet_ids         = [data.aws_subnet.coordinator.id]
    security_group_ids = [aws_security_group.gateway.id]
  }
  environment {
    variables = {
      COORDINATOR_IP   = data.aws_instance.coordinator.private_ip
      OPERATOR_ORIGIN  = aws_apigatewayv2_api.operator.api_endpoint
      PRINCIPAL_PREFIX = local.principal_prefix
    }
  }
  depends_on = [aws_iam_role_policy_attachment.vpc, aws_cloudwatch_log_group.gateway]
}
resource "aws_apigatewayv2_integration" "gateway" {
  api_id                 = aws_apigatewayv2_api.operator.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.gateway.invoke_arn
  payload_format_version = "2.0"
  timeout_milliseconds   = 29000
}
resource "aws_apigatewayv2_route" "iam" {
  api_id             = aws_apigatewayv2_api.operator.id
  route_key          = "ANY /operator/{action}"
  authorization_type = "AWS_IAM"
  target             = "integrations/${aws_apigatewayv2_integration.gateway.id}"
}
resource "aws_apigatewayv2_route" "browser" {
  api_id             = aws_apigatewayv2_api.operator.id
  route_key          = "$default"
  authorization_type = "NONE"
  target             = "integrations/${aws_apigatewayv2_integration.gateway.id}"
}
resource "aws_apigatewayv2_stage" "operator" {
  api_id      = aws_apigatewayv2_api.operator.id
  name        = "$default"
  auto_deploy = true
  default_route_settings {
    throttling_burst_limit = 40
    throttling_rate_limit  = 20
  }
}
resource "aws_lambda_permission" "gateway" {
  statement_id  = "OperatorGatewayOnly"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.gateway.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.operator.execution_arn}/*/*"
}

output "url" { value = aws_apigatewayv2_api.operator.api_endpoint }
output "gateway_security_group" { value = aws_security_group.gateway.id }
output "operator_role_arn" { value = var.operator_role_arn }
