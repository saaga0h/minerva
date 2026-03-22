# minerva-trigger.hcl
# Fires the Minerva pipeline trigger once per hour.
# All daemons must be running before this fires — they connect to Mosquitto
# on startup and paho uses CleanSession=true, so messages published before
# a primitive connects are lost.
#
# Force an immediate run:
#   curl -X POST http://192.168.10.42:4646/v1/job/minerva-trigger/periodic/force

job "minerva-trigger" {
  datacenters = ["the-collective"]
  type        = "batch"

  constraint {
    attribute = "${meta.gpu}"
    operator  = "!="
    value     = "true"
  }

  periodic {
    crons            = ["0 * * * *"]
    prohibit_overlap = true
    time_zone        = "UTC"
  }

  group "trigger" {
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
        args    = ["-c", "mosquitto_pub -h $MQTT_HOST -p $MQTT_PORT -t minerva/pipeline/trigger -m '{}'"]
      }

      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
MQTT_HOST={{ .Data.data.MQTT_HOST }}
MQTT_PORT={{ .Data.data.MQTT_PORT }}
{{ end }}
EOT
      }

      vault { policies = ["minerva"] }

      resources {
        cpu    = 50
        memory = 32
      }
    }
  }
}
