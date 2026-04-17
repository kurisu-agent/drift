package wire

// Method names for the JSON-RPC 2.0 surface. Both the drift client and the
// lakitu server reference these constants so a typo surfaces at compile time
// rather than on the wire. The catalog mirrors plans/PLAN.md § Method catalog.
const (
	MethodServerVersion = "server.version"
	MethodServerInit    = "server.init"

	MethodKartNew     = "kart.new"
	MethodKartStart   = "kart.start"
	MethodKartStop    = "kart.stop"
	MethodKartRestart = "kart.restart"
	MethodKartDelete  = "kart.delete"
	MethodKartList    = "kart.list"
	MethodKartInfo    = "kart.info"
	MethodKartEnable  = "kart.enable"
	MethodKartDisable = "kart.disable"
	MethodKartLogs    = "kart.logs"

	MethodCharacterAdd    = "character.add"
	MethodCharacterList   = "character.list"
	MethodCharacterShow   = "character.show"
	MethodCharacterRemove = "character.remove"

	MethodChestSet    = "chest.set"
	MethodChestGet    = "chest.get"
	MethodChestList   = "chest.list"
	MethodChestRemove = "chest.remove"

	MethodTuneList   = "tune.list"
	MethodTuneShow   = "tune.show"
	MethodTuneSet    = "tune.set"
	MethodTuneRemove = "tune.remove"

	MethodConfigShow = "config.show"
	MethodConfigSet  = "config.set"
)
