package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/goburrow/modbus"
)

var isReplying = false

func main() {
	portPtr := flag.String("port", "/dev/ttyUSB0", "Path to your RS485 device")
	baudRatePtr := flag.Int("baudRate", 9600, "Port baud rate")
	mqttServerPtr := flag.String("mqttServer", "tcp://127.0.0.1:1883", "MQTT server address")
	topicPtr := flag.String("topic", "pv_inverter", "MQTT topic prefix")
	slavePtr := flag.Int("slave", 1, "Slave number")

	flag.Parse()

	mqttClient := connectMqtt(*mqttServerPtr, *topicPtr)
	pushHomeAssistantConfig(mqttClient, *topicPtr)

	handler := modbus.NewRTUClientHandler(*portPtr)
	handler.BaudRate = *baudRatePtr
	handler.DataBits = 8
	handler.Parity = "N"
	handler.StopBits = 1
	handler.SlaveId = byte(*slavePtr)
	handler.Timeout = 1 * time.Second

	handler.Connect()
	defer handler.Close()

	client := modbus.NewClient(handler)

	var lastSuccess time.Time

	type readerFunc func(client modbus.Client, mqttClient mqtt.Client, topic string) error
	funcs := []readerFunc{readPvPower, readBatteryCharge, readBatteryDischarge, readBatteryVoltage,
		readBatteryCapacity, readBatteryPower, readLoadPower, readInverterPower, readBackupLoadPower}

	for {
		for _, fn := range funcs {
			err := fn(client, mqttClient, *topicPtr)
			if err != nil {
				log.Println("Error reading data:", err)
			} else {
				lastSuccess = time.Now()
			}
		}

		if time.Since(lastSuccess) < 5*time.Second {
			if !isReplying {
				isReplying = true

				log.Println("Inverter is now replying to our requests")

				mqttClient.Publish(*topicPtr+"/status", 0, true, "online").Wait()
			}
		} else {
			if isReplying {
				isReplying = false

				log.Println("Inverter is not replying to our requests anymore")

				mqttClient.Publish(*topicPtr+"/status", 0, true, "offline").Wait()
			}
		}

		time.Sleep(5 * time.Second)
	}
}

func lowHighToUint(regBytes []byte) uint32 {
	var result uint32

	// low word, then high word, but words are in big endian

	result = uint32(regBytes[1])
	result |= uint32(regBytes[0]) << 8
	result |= uint32(regBytes[3]) << 16
	result |= uint32(regBytes[2]) << 24

	return result
}

func wordToUint16(regBytes []byte) uint16 {
	var result uint16

	result = uint16(regBytes[1])
	result |= uint16(regBytes[0]) << 8

	return result
}

func wordToInt16(regBytes []byte) int16 {
	var result int16

	result = int16(regBytes[1]) & 0xff
	result |= int16(regBytes[0]) << 8

	return result
}

func readHundredsWhValue(client modbus.Client, mqttClient mqtt.Client, topic string, reg uint16) error {
	results, err := client.ReadHoldingRegisters(reg, 2)
	if err != nil {
		return err
	}

	value := uint64(lowHighToUint(results)) * 100 // convert from 0.1 kWh to Wh

	if value == 0 {
		// The inverter sometimes send zero values at startup,
		// which messes up stats in Home Assistant
		log.Println("Not publishing 0 value to topic", topic)
		return nil
	}

	log.Println("Going to publish to topic", topic, value)
	mqttClient.Publish(topic, 0, true, fmt.Sprint(value)).Wait()

	return nil
}

func readPvPower(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	return readHundredsWhValue(client, mqttClient, topic+"/total_pv_power", RegTotalPvPowerLow)
}

func readBatteryCharge(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	return readHundredsWhValue(client, mqttClient, topic+"/total_battery_charge", RegTotalBatteryChargeLow)
}

func readBatteryDischarge(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	return readHundredsWhValue(client, mqttClient, topic+"/total_battery_discharge", RegTotalBatteryDischargeLow)
}

func readBatteryVoltage(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	results, err := client.ReadHoldingRegisters(RegBatteryVoltage, 1)
	if err != nil {
		return err
	}

	value := float32(wordToUint16(results)) / 100
	mqttClient.Publish(topic+"/battery_voltage", 0, true, fmt.Sprint(value)).Wait()

	return nil
}

func readBatteryCapacity(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	results, err := client.ReadHoldingRegisters(RegBatteryCapacity, 1)
	if err != nil {
		return err
	}

	pct := wordToUint16(results)
	mqttClient.Publish(topic+"/battery_capacity_pct", 0, true, fmt.Sprint(pct)).Wait()

	return nil
}

func readBatteryPower(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	results, err := client.ReadHoldingRegisters(RegBatteryPower, 1)
	if err != nil {
		return err
	}

	value := float32(wordToInt16(results))
	mqttClient.Publish(topic+"/battery_power", 0, true, fmt.Sprint(value)).Wait()

	return nil
}

func readLoadPower(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	results, err := client.ReadHoldingRegisters(RegLoadPowerTotal, 1)
	if err != nil {
		return err
	}

	value := float32(wordToUint16(results))
	mqttClient.Publish(topic+"/load_power", 0, true, fmt.Sprint(value)).Wait()

	return nil
}

func readBackupLoadPower(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	results, err := client.ReadHoldingRegisters(RegBackupLoadPowerTotal, 1)
	if err != nil {
		return err
	}

	value := float32(wordToUint16(results))
	mqttClient.Publish(topic+"/backup_load_power", 0, true, fmt.Sprint(value)).Wait()

	return nil
}

func readInverterPower(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	results, err := client.ReadHoldingRegisters(RegInverterOutputPower1, 4)
	if err != nil {
		return err
	}

	for i := 1; i <= 3; i++ {
		watts := wordToInt16(results[i*2-2 : i*2])
		mqttClient.Publish(fmt.Sprint(topic, "/pv_power_l", i), 0, true, fmt.Sprint(watts)).Wait()
	}

	totalWatts := wordToInt16(results[6:])
	mqttClient.Publish(topic+"/pv_power_all", 0, true, fmt.Sprint(totalWatts)).Wait()

	return nil
}

func connectMqtt(address, topic string) mqtt.Client {
	opts := mqtt.NewClientOptions().AddBroker(address).SetClientID("hass_deye")
	opts.SetKeepAlive(2 * time.Second)
	opts.SetPingTimeout(1 * time.Second)
	opts.SetWill(topic+"/status", "offline", 0, true)
	opts.OnConnect = func(client mqtt.Client) {
		log.Println("MQTT connected")

		stateStr := "online"
		if !isReplying {
			stateStr = "offline"
		}

		client.Publish(topic+"/status", 0, true, stateStr).Wait()
	}
	opts.OnConnectionLost = func(client mqtt.Client, err error) {
		log.Println("MQTT connection lost:", err)
	}

	c := mqtt.NewClient(opts)
	if token := c.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalln("Failed to connect to MQTT server:", token.Error())
	}

	return c
}

type HassAutoconfig struct {
	DeviceClass       string               `json:"dev_cla"`
	UnitOfMeasurement string               `json:"unit_of_meas"`
	Name              string               `json:"name"`
	StatusTopic       string               `json:"stat_t"`
	AvailabilityTopic string               `json:"avty_t"`
	UniqueID          string               `json:"uniq_id"`
	StateClass        string               `json:"stat_cla"`
	Device            HassAutoconfigDevice `json:"dev"`
}

type HassAutoconfigDevice struct {
	IDs  string `json:"ids"`
	Name string `json:"name"`
}

func pushHomeAssistantConfig(mqttClient mqtt.Client, topic string) {
	var autoconf HassAutoconfig

	hostname, _ := os.Hostname()

	autoconf.DeviceClass = "energy"
	autoconf.StateClass = "total_increasing"
	autoconf.UnitOfMeasurement = "Wh"
	autoconf.Name = "total_pv_power"
	autoconf.AvailabilityTopic = topic + "/status"
	autoconf.StatusTopic = topic + "/total_pv_power"
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)
	autoconf.Device.IDs = hostname
	autoconf.Device.Name = hostname

	jsonBytes, _ := json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/total_pv_power/config", 0, true, string(jsonBytes)).Wait()

	///

	autoconf.Name = "total_battery_charge"
	autoconf.StatusTopic = topic + "/total_battery_charge"
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)

	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/total_battery_charge/config", 0, true, string(jsonBytes)).Wait()

	///

	autoconf.Name = "total_battery_discharge"
	autoconf.StatusTopic = topic + "/total_battery_discharge"
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)

	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/total_battery_discharge/config", 0, true, string(jsonBytes)).Wait()

	///

	autoconf.Name = "battery_voltage"
	autoconf.DeviceClass = "voltage"
	autoconf.UnitOfMeasurement = "V"
	autoconf.StatusTopic = topic + "/battery_voltage"
	autoconf.StateClass = "measurement"
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)

	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/battery_voltage/config", 0, true, string(jsonBytes)).Wait()

	///

	autoconf.Name = "battery_capacity_pct"
	autoconf.DeviceClass = "battery"
	autoconf.UnitOfMeasurement = "%"
	autoconf.StatusTopic = topic + "/battery_capacity_pct"
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)

	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/battery_capacity_pct/config", 0, true, string(jsonBytes)).Wait()

	///

	for i := 1; i <= 3; i++ {
		autoconf.Name = fmt.Sprint("pv_power_l", i)
		autoconf.DeviceClass = "power"
		autoconf.StateClass = "measurement"
		autoconf.UnitOfMeasurement = "W"
		autoconf.StatusTopic = topic + "/" + autoconf.Name
		autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)

		jsonBytes, _ = json.Marshal(&autoconf)
		mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/"+autoconf.Name+"/config", 0, true, string(jsonBytes)).Wait()
	}

	autoconf.Name = "pv_power_all"
	autoconf.StatusTopic = topic + "/" + autoconf.Name
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)
	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/"+autoconf.Name+"/config", 0, true, string(jsonBytes)).Wait()

	///

	autoconf.Name = "battery_power"
	autoconf.StatusTopic = topic + "/" + autoconf.Name
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)
	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/"+autoconf.Name+"/config", 0, true, string(jsonBytes)).Wait()

	///

	autoconf.Name = "load_power"
	autoconf.StatusTopic = topic + "/" + autoconf.Name
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)
	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/"+autoconf.Name+"/config", 0, true, string(jsonBytes)).Wait()

	///

	autoconf.Name = "backup_load_power"
	autoconf.StatusTopic = topic + "/" + autoconf.Name
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)
	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/"+autoconf.Name+"/config", 0, true, string(jsonBytes)).Wait()
}
