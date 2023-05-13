package main

const (
	RegTotalPvPowerLow  uint16 = 534
	RegTotalPvPowerHigh uint16 = 535

	RegTotalGridBuyLow   uint16 = 522
	RegTotalGridBuyHigh  uint16 = 523
	RegTotalGridSellLow  uint16 = 524
	RegTotalGridSellHigh uint16 = 525

	RegTotalBatteryChargeLow  uint16 = 516
	RegTotalBatteryChargeHigh uint16 = 517

	RegTotalBatteryDischargeLow  uint16 = 518
	RegTotalBatteryDischargeHigh uint16 = 519

	RegBatteryVoltage  uint16 = 587
	RegBatteryCapacity uint16 = 588
	RegBatteryPower    uint16 = 590

	RegGridPowerTotal uint16 = 625

	RegInverterOutputPower1     uint16 = 633
	RegInverterOutputPower2     uint16 = 634
	RegInverterOutputPower3     uint16 = 635
	RegInverterOutputPowerTotal uint16 = 636

	RegBackupLoadPowerTotal uint16 = 643
	RegLoadPowerTotal       uint16 = 653
)

const (
	RegPowerEnable           uint16 = 80
	RegSolarSellPower        uint16 = 340
	RegGridCharge            uint16 = 130
	RegEnergyManagementModel uint16 = 141
	RegLimitControlFunction  uint16 = 142
	RegSolarSell             uint16 = 145
)

// RegLimitControlFunction
const (
	ModeSellingFirst     uint16 = 0
	ModeZeroExportToLoad uint16 = 1
	ModeZeroExportToCT   uint16 = 2
)

// RegEnergyManagementModel
const (
	ManagementBatteryFirst uint16 = 2
	ManagementLoadFirst    uint16 = 3
)
