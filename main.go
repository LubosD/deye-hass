package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/goburrow/modbus"
)

var isReplying = false
var mutex sync.Mutex

func main() {
	portPtr := flag.String("port", "/dev/ttyUSB0", "Path to your RS485 device")
	baudRatePtr := flag.Int("baudRate", 9600, "Port baud rate")
	mqttServerPtr := flag.String("mqttServer", "tcp://127.0.0.1:1883", "MQTT server address")
	topicPtr := flag.String("topic", "pv_inverter", "MQTT topic prefix")
	slavePtr := flag.Int("slave", 1, "Slave number")

	flag.Parse()

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

	mqttClient := connectMqtt(*mqttServerPtr, *topicPtr, client)
	pushHomeAssistantConfig(mqttClient, *topicPtr)

	var lastSuccess time.Time

	type readerFunc func(client modbus.Client, mqttClient mqtt.Client, topic string) error
	funcs := []readerFunc{
		readDeciWh("total_pv_power", RegTotalPvPowerLow),
		readDeciWh("total_battery_charge", RegTotalBatteryChargeLow),
		readDeciWh("total_battery_discharge", RegTotalBatteryDischargeLow),
		readDeciWh("total_grid_buy", RegTotalGridBuyLow),
		readDeciWh("total_grid_sell", RegTotalGridSellLow),
		readUint("max_solar_sell_power", RegSolarSellPower),
		readUint("battery_capacity_pct", RegBatteryCapacity),
		readInt("battery_power", RegBatteryPower),
		readUint("backup_load_power", RegBackupLoadPowerTotal),
		readUint("load_power", RegLoadPowerTotal),
		readInt("grid_power", RegGridPowerTotal),
		readBool("solar_sell", RegSolarSell),
		readBool("grid_charge", RegGridCharge),
		readBatteryVoltage,
		readInverterPower,
		readInverterMode,
	}

	// mqttClient.Subscribe(*topicPtr+"/power_enable", 0, func(mc mqtt.Client, message mqtt.Message) {
	//	handlePowerEnable(mc, message, client)
	//})

	for {
		for _, fn := range funcs {
			mutex.Lock()
			err := fn(client, mqttClient, *topicPtr)
			mutex.Unlock()
			if err != nil {
				log.Println("Error reading data:", err)
			} else {
				lastSuccess = time.Now()
				// time.Sleep(250 * time.Millisecond)
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

		// dumpManagementConfig(client)

		time.Sleep(500 * time.Millisecond)
	}
}

func subscribeTopics(client modbus.Client, mqttClient mqtt.Client, topic string) {
	mqttClient.Subscribe(topic+"/max_solar_sell_power_set", 0, func(mc mqtt.Client, message mqtt.Message) {
		handleSetSolarSellPower(mc, message, client)
	}).Wait()

	mqttClient.Subscribe(topic+"/inverter_mode_set", 0, func(mc mqtt.Client, message mqtt.Message) {
		handleSetInverterMode(mc, message, client)
	}).Wait()

	handleWriteBool(client, mqttClient, topic+"/solar_sell_set", RegSolarSell)
	handleWriteBool(client, mqttClient, topic+"/grid_charge_set", RegGridCharge)
}

func handleWriteBool(client modbus.Client, mqttClient mqtt.Client, topic string, reg uint16) {
	mqttClient.Subscribe(topic, 0, func(mc mqtt.Client, message mqtt.Message) {
		defer message.Ack()

		text := string(message.Payload())

		var value uint16

		switch strings.ToLower(text) {
		case "on":
			value = 1
		case "off":
			value = 0
		default:
			log.Println(topic + ": Invalid value: " + text)
			return
		}

		mutex.Lock()
		_, err := client.WriteMultipleRegisters(reg, 1, uint16ToWord(value))
		mutex.Unlock()

		if err != nil {
			log.Println(topic+": Error writing register:", err)
		} else {
			log.Println(topic + ": write OK")
		}
	}).Wait()
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

func uint16ToWord(value uint16) []byte {
	var result [2]byte

	result[1] = byte(value & 0xff)
	result[0] = byte(value >> 8)

	return result[:]
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

func readDeciWh(subtopic string, reg uint16) func(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	return func(client modbus.Client, mqttClient mqtt.Client, topic string) error {
		return readHundredsWhValue(client, mqttClient, topic+"/"+subtopic, reg)
	}
}

func readUint(subtopic string, reg uint16) func(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	return func(client modbus.Client, mqttClient mqtt.Client, topic string) error {
		results, err := client.ReadHoldingRegisters(reg, 1)
		if err != nil {
			return err
		}

		val := wordToUint16(results)
		mqttClient.Publish(topic+"/"+subtopic, 0, true, strconv.FormatUint(uint64(val), 10)).Wait()

		return nil
	}
}

func readInt(subtopic string, reg uint16) func(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	return func(client modbus.Client, mqttClient mqtt.Client, topic string) error {
		results, err := client.ReadHoldingRegisters(reg, 1)
		if err != nil {
			return err
		}

		val := wordToInt16(results)
		mqttClient.Publish(topic+"/"+subtopic, 0, true, strconv.FormatInt(int64(val), 10)).Wait()

		return nil
	}
}

func readBool(subtopic string, reg uint16) func(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	return func(client modbus.Client, mqttClient mqtt.Client, topic string) error {
		results, err := client.ReadHoldingRegisters(reg, 1)
		if err != nil {
			return err
		}

		value := wordToUint16(results)
		var valueStr string

		if value == 0 {
			valueStr = "OFF"
		} else {
			valueStr = "ON"
		}

		mqttClient.Publish(topic+"/"+subtopic, 0, true, valueStr).Wait()

		return nil
	}
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

func readInverterMode(client modbus.Client, mqttClient mqtt.Client, topic string) error {
	results, err := client.ReadHoldingRegisters(RegLimitControlFunction, 1)
	if err != nil {
		return err
	}

	value := wordToUint16(results)
	var valueStr string

	switch value {
	case ModeSellingFirst:
		valueStr = InverterModeSellingFirstString
	case ModeZeroExportToLoad:
		valueStr = InverterModeZeroExportToLoadString
	case ModeZeroExportToCT:
		valueStr = InverterModeZeroExportToCTString
	default:
		valueStr = "UNKNOWN"
	}

	mqttClient.Publish(topic+"/inverter_mode", 0, true, valueStr).Wait()

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

func connectMqtt(address, topic string, modbusClient modbus.Client) mqtt.Client {
	opts := mqtt.NewClientOptions().
		AddBroker(address).
		SetClientID("hass_deye").
		SetKeepAlive(2*time.Second).
		SetPingTimeout(1*time.Second).
		SetWill(topic+"/status", "offline", 0, true).
		SetAutoReconnect(true).
		SetResumeSubs(true).
		SetOrderMatters(false)

	opts.OnConnect = func(client mqtt.Client) {
		log.Println("MQTT connected")

		stateStr := "online"
		if !isReplying {
			stateStr = "offline"
		}

		client.Publish(topic+"/status", 0, true, stateStr).Wait()

		subscribeTopics(modbusClient, client, topic)
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
	DeviceClass       string               `json:"dev_cla,omitempty"`
	UnitOfMeasurement string               `json:"unit_of_meas,omitempty"`
	Name              string               `json:"name"`
	StatusTopic       string               `json:"stat_t"`
	CommandTopic      string               `json:"cmd_t,omitempty"`
	AvailabilityTopic string               `json:"avty_t"`
	UniqueID          string               `json:"uniq_id"`
	StateClass        string               `json:"stat_cla,omitempty"`
	Device            HassAutoconfigDevice `json:"dev"`

	// For number
	Min *int `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`

	// For select
	Ops []string `json:"ops,omitempty"`
}

func intPtr(value int) *int {
	return &value
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

	autoconf.Name = "total_grid_buy"
	autoconf.StatusTopic = topic + "/total_grid_buy"
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)

	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/total_grid_buy/config", 0, true, string(jsonBytes)).Wait()

	///

	autoconf.Name = "total_grid_sell"
	autoconf.StatusTopic = topic + "/total_grid_sell"
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)

	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/total_grid_sell/config", 0, true, string(jsonBytes)).Wait()

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

	///

	autoconf.Name = "max_solar_sell_power"
	autoconf.StatusTopic = topic + "/" + autoconf.Name
	autoconf.CommandTopic = autoconf.StatusTopic + "_set"
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)
	autoconf.Max = intPtr(20000)
	autoconf.Min = intPtr(0)
	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/number/inverter_"+hostname+"/"+autoconf.Name+"/config", 0, true, string(jsonBytes)).Wait()
	autoconf.CommandTopic = ""
	autoconf.Min = nil
	autoconf.Max = nil

	///

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

	///

	autoconf.Name = "grid_power"
	autoconf.StatusTopic = topic + "/" + autoconf.Name
	autoconf.UniqueID = fmt.Sprint(topic, ".", hostname, ".", autoconf.Name)
	jsonBytes, _ = json.Marshal(&autoconf)
	mqttClient.Publish("homeassistant/sensor/inverter_"+hostname+"/"+autoconf.Name+"/config", 0, true, string(jsonBytes)).Wait()

	///

	modeSelect := HassAutoconfig{
		Device:            autoconf.Device,
		Name:              "inverter_mode",
		AvailabilityTopic: topic + "/status",
		StatusTopic:       topic + "/inverter_mode",
		CommandTopic:      topic + "/inverter_mode_set",
		UniqueID:          fmt.Sprint(topic, ".", hostname, ".inverter_mode"),
		Ops: []string{
			InverterModeSellingFirstString,
			InverterModeZeroExportToLoadString,
			InverterModeZeroExportToCTString,
		},
	}
	jsonBytes, _ = json.Marshal(&modeSelect)
	mqttClient.Publish("homeassistant/select/inverter_"+hostname+"/"+modeSelect.Name+"/config", 0, true, string(jsonBytes)).Wait()

	///

	solarSellSwitch := HassAutoconfig{
		Device:            autoconf.Device,
		Name:              "solar_sell",
		AvailabilityTopic: topic + "/status",
		StatusTopic:       topic + "/solar_sell",
		CommandTopic:      topic + "/solar_sell_set",
		UniqueID:          fmt.Sprint(topic, ".", hostname, ".solar_sell"),
	}
	jsonBytes, _ = json.Marshal(&solarSellSwitch)
	mqttClient.Publish("homeassistant/switch/inverter_"+hostname+"/"+solarSellSwitch.Name+"/config", 0, true, string(jsonBytes)).Wait()

	///

	gridChargeSwitch := HassAutoconfig{
		Device:            autoconf.Device,
		Name:              "grid_charge",
		AvailabilityTopic: topic + "/status",
		StatusTopic:       topic + "/grid_charge",
		CommandTopic:      topic + "/grid_charge_set",
		UniqueID:          fmt.Sprint(topic, ".", hostname, ".grid_charge"),
	}
	jsonBytes, _ = json.Marshal(&gridChargeSwitch)
	mqttClient.Publish("homeassistant/switch/inverter_"+hostname+"/"+gridChargeSwitch.Name+"/config", 0, true, string(jsonBytes)).Wait()
}

func handleSetSolarSellPower(client mqtt.Client, message mqtt.Message, modbusClient modbus.Client) {
	defer message.Ack()

	text := string(message.Payload())
	value, err := strconv.Atoi(text)

	if err != nil {
		log.Println("handleSetSolarSellPower: Invalid value:", err)
		return
	}

	mutex.Lock()
	_, err = modbusClient.WriteMultipleRegisters(RegSolarSellPower, 1, uint16ToWord(uint16(value)))
	//_, err = modbusClient.WriteSingleRegister(RegSolarSellPower, uint16(value))
	mutex.Unlock()

	if err != nil {
		log.Println("handleSetSolarSellPower: Error writing register:", err)
	} else {
		log.Println("handleSetSolarSellPower: write OK")
	}
}

func handleSetInverterMode(client mqtt.Client, message mqtt.Message, modbusClient modbus.Client) {
	defer message.Ack()

	text := string(message.Payload())
	var modeValue uint16

	switch text {
	case InverterModeSellingFirstString:
		modeValue = ModeSellingFirst
	case InverterModeZeroExportToLoadString:
		modeValue = ModeZeroExportToLoad
	case InverterModeZeroExportToCTString:
		modeValue = ModeZeroExportToCT
	default:
		log.Println("handleSetInverterMode: Invalid value: " + text)
		return
	}

	mutex.Lock()
	_, err := modbusClient.WriteMultipleRegisters(RegLimitControlFunction, 1, uint16ToWord(modeValue))
	mutex.Unlock()

	if err != nil {
		log.Println("handleSetInverterMode: Error writing register:", err)
	} else {
		log.Println("handleSetInverterMode: write OK")
	}
}

func handleSetSolarSell(client mqtt.Client, message mqtt.Message, modbusClient modbus.Client) {
	defer message.Ack()

	text := string(message.Payload())

	var value uint16

	switch strings.ToLower(text) {
	case "on":
		value = 1
	case "off":
		value = 0
	default:
		log.Println("handleSetSolarSell: Invalid value: " + text)
		return
	}

	mutex.Lock()
	_, err := modbusClient.WriteMultipleRegisters(RegSolarSell, 1, uint16ToWord(value))
	mutex.Unlock()

	if err != nil {
		log.Println("handleSetSolarSell: Error writing register:", err)
	} else {
		log.Println("handleSetSolarSell: write OK")
	}
}

/*
func handlePowerEnable(client mqtt.Client, message mqtt.Message, modbusClient modbus.Client) {
	var err error
	text := string(message.Payload())

	switch text {
	case "1":
		fallthrough
	case "on":
		_, err = modbusClient.WriteSingleRegister(RegPowerEnable, 1)
	case "0":
		fallthrough
	case "off":
		_, err = modbusClient.WriteSingleRegister(RegPowerEnable, 0)
	}

	if err != nil {
		log.Println("handlePowerEnable error:", err)
	} else {
		log.Println("handlePowerEnable OK")
	}
}
*/

/*
func dumpManagementConfig(modbusClient modbus.Client) error {
	results, err := modbusClient.ReadHoldingRegisters(141, 1)
	if err != nil {
		return err
	}

	value := wordToUint16(results)
	log.Println("Energy management model: " + strconv.FormatUint(uint64(value), 2))

	results, err = modbusClient.ReadHoldingRegisters(142, 1)
	if err != nil {
		return err
	}

	value = wordToUint16(results)
	log.Println("Limit control function: " + strconv.FormatUint(uint64(value), 2))

	results, err = modbusClient.ReadHoldingRegisters(145, 1)
	if err != nil {
		return err
	}

	value = wordToUint16(results)
	log.Println("Solar sell: " + strconv.FormatUint(uint64(value), 2))

	return nil
}
*/

/*

Selling first:
141 -> 1b
142 -> 0b
145 -> 1b

Zero export to CT w/ solar sell:
141 -> 1b
142 -> 10b (aka "extraposition enabled")
145 -> 1b

Zero export to Load w/solar sell:
141 -> 1b
142 -> 1b
145 -> 1b

*/
