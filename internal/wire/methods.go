package wire

// Both drift and lakitu reference these constants so a typo surfaces at
// compile time rather than on the wire.
const (
	MethodServerVersion = "server.version"
	MethodServerInfo    = "server.info"
	MethodServerInit    = "server.init"
	MethodServerVerify  = "server.verify"

	MethodKartNew         = "kart.new"
	MethodKartStart       = "kart.start"
	MethodKartStop        = "kart.stop"
	MethodKartRestart     = "kart.restart"
	MethodKartDelete      = "kart.delete"
	MethodKartList        = "kart.list"
	MethodKartInfo        = "kart.info"
	MethodKartEnable      = "kart.enable"
	MethodKartDisable     = "kart.disable"
	MethodKartLogs        = "kart.logs"
	MethodKartSessionEnv  = "kart.session_env"
	MethodKartMigrateList = "kart.migrate_list"

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

	MethodRunList    = "run.list"
	MethodRunResolve = "run.resolve"
)

// Methods returns the catalog in source order. Keep in sync with the
// const block — the cost of duplicating the names is paid here so
// consumers (cliscript, clihelp, docs) don't have to grep.
func Methods() []string {
	return []string{
		MethodServerVersion, MethodServerInfo, MethodServerInit, MethodServerVerify,
		MethodKartNew, MethodKartStart, MethodKartStop, MethodKartRestart,
		MethodKartDelete, MethodKartList, MethodKartInfo,
		MethodKartEnable, MethodKartDisable, MethodKartLogs,
		MethodKartSessionEnv,
		MethodKartMigrateList,
		MethodCharacterAdd, MethodCharacterList, MethodCharacterShow, MethodCharacterRemove,
		MethodChestSet, MethodChestGet, MethodChestList, MethodChestRemove,
		MethodTuneList, MethodTuneShow, MethodTuneSet, MethodTuneRemove,
		MethodConfigShow, MethodConfigSet,
		MethodRunList, MethodRunResolve,
	}
}
