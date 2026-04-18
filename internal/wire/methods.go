package wire

// Method names for the JSON-RPC 2.0 surface. Both the drift client and the
// lakitu server reference these constants so a typo surfaces at compile time
// rather than on the wire.
const (
	MethodServerVersion = "server.version"
	MethodServerInit    = "server.init"
	MethodServerVerify  = "server.verify"

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

// Methods returns every JSON-RPC method name in the catalog. The order
// tracks this file's source order; callers that want a stable presentation
// should sort the result. Keep this list in sync with the const block above
// — it is the one place we pay the price of declaring the names twice so
// consumers (cliscript, clihelp, docs) don't have to grep the block.
func Methods() []string {
	return []string{
		MethodServerVersion, MethodServerInit, MethodServerVerify,
		MethodKartNew, MethodKartStart, MethodKartStop, MethodKartRestart,
		MethodKartDelete, MethodKartList, MethodKartInfo,
		MethodKartEnable, MethodKartDisable, MethodKartLogs,
		MethodCharacterAdd, MethodCharacterList, MethodCharacterShow, MethodCharacterRemove,
		MethodChestSet, MethodChestGet, MethodChestList, MethodChestRemove,
		MethodTuneList, MethodTuneShow, MethodTuneSet, MethodTuneRemove,
		MethodConfigShow, MethodConfigSet,
	}
}
