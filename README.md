# Deye client for Home Assistant

This Go app talks to Deye inverters (new models as of 2022) and pushes data into MQTT (e.g. for Home Assistant)

Based on Modbus register documentation saved in [this file](modbus_deye.docx).

## Provided Data

* Total PV energy
* Total battery charge
* Total battery discharge
* Battery voltage
* Battery capacity (%)
* Per-phase and sum of current power from inverter

## Usage

```
Usage of ./hass-deye:
  -baudRate int
        Port baud rate (default 9600)
  -mqttServer string
        MQTT server address (default "tcp://127.0.0.1:1883")
  -port string
        Path to your RS485 device (default "/dev/ttyUSB0")
  -slave int
        Slave number (default 1)
  -topic string
        MQTT topic prefix (default "pv_inverter")
```
