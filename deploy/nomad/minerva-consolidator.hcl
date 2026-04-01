# minerva-consolidator.hcl
# Runs after Journal's brief queries have had time to complete.
# Picks the single most interesting work from recent brief sessions
# and publishes a ConsolidatorDigest for the notifier to deliver.
#
# Scheduled at 07:45 UTC — after Journal briefs (typically overnight)
# and before morning reading time.
#
# Force an immediate run:
#   curl -X POST http://namad.server:4646/v1/job/minerva-consolidator/periodic/force

job "minerva-consolidator" {
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

  periodic {
    crons            = ["15 8 * * *"]
    prohibit_overlap = true
    time_zone        = "Europe/Helsinki"
  }

  group "consolidator" {
    restart {
      attempts = 2
      interval = "5m"
      delay    = "15s"
      mode     = "fail"
    }

    task "run" {
      driver = "raw_exec"

      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/consolidator && exec ${NOMAD_TASK_DIR}/consolidator"]
      }

      artifact {
        source      = "${NOMAD_META_artifact_base}/${attr.cpu.arch}/consolidator"
        destination = "local/consolidator"
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
CONSOLIDATOR_LOOKBACK_HOURS={{ .Data.data.CONSOLIDATOR_LOOKBACK_HOURS }}
CONSOLIDATOR_DEDUP_HOURS={{ .Data.data.CONSOLIDATOR_DEDUP_HOURS }}
CONSOLIDATOR_MIN_SCORE={{ .Data.data.CONSOLIDATOR_MIN_SCORE }}
CONSOLIDATOR_TOP_N={{ .Data.data.CONSOLIDATOR_TOP_N }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }

      vault { policies = ["minerva"] }

      resources {
        cpu    = 50
        memory = 64
      }
    }
  }
}
