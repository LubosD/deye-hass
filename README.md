# Deye client for Home Assistant

This Go app talks to Deye inverters (new models as of 2022) and pushes data into MQTT (e.g. for Home Assistant)

Based on Modbus register documentation saved in [this file](modbus_deye.docx).

## Provided Data

* Total PV power
* Total battery charge
* Total battery discharge
* Battery voltage
* Battery capacity (%)
