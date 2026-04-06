# minerva-reembed-works.hcl
# One-shot backfill: embeds all works with embedding IS NULL via Forge.
# Run once after FORGE_ENABLED is turned on in production.
#
# Deploy (runs immediately, exits when done):
#   nomad job run deploy/nomad/minerva-reembed-works.hcl
#
# Monitor:
#   nomad job status minerva-reembed-works
#   nomad alloc logs <alloc-id>

job "minerva-reembed-works" {
  datacenters = ["the-collective"]
  type        = "batch"

  meta {
    artifact_base = "${ARTIFACT_BASE}"
  }

  constraint {
    attribute = "${meta.gpu}"
    operator  = "!="
    value     = "true"
  }

  group "reembed" {
    restart {
      attempts = 1
      interval = "10m"
      delay    = "30s"
      mode     = "fail"
    }

    task "run" {
      driver = "raw_exec"

      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/reembed-works && exec ${NOMAD_TASK_DIR}/reembed-works"]
      }

      artifact {
        source      = "${NOMAD_META_artifact_base}/${attr.cpu.arch}/reembed-works"
        destination = "local/reembed-works"
        mode        = "file"
      }

      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
DB_HOST={{ .Data.data.DB_HOST }}
DB_PORT={{ .Data.data.DB_PORT }}
DB_USER={{ .Data.data.DB_USER }}
DB_PASSWORD={{ .Data.data.DB_PASSWORD }}
DB_NAME={{ .Data.data.DB_NAME }}
DB_SSLMODE={{ .Data.data.DB_SSLMODE }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
MQTT_USER={{ .Data.data.MQTT_USER }}
MQTT_PASSWORD={{ .Data.data.MQTT_PASSWORD }}
FORGE_ENABLED={{ .Data.data.FORGE_ENABLED }}
FORGE_EMBED_MODEL={{ .Data.data.FORGE_EMBED_MODEL }}
FORGE_TIMEOUT={{ .Data.data.FORGE_TIMEOUT }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }

      vault { policies = ["minerva"] }

      resources {
        cpu    = 100
        memory = 128
      }
    }
  }
}
