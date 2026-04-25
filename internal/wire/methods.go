package wire

// Both drift and lakitu reference these constants so a typo surfaces at
// compile time rather than on the wire.
const (
	MethodServerVersion = "server.version"
	MethodServerInfo    = "server.info"
	MethodServerInit    = "server.init"
	MethodServerVerify  = "server.verify"
	// MethodServerStatus is the combined hot-path RPC for `drift status`:
	// returns server.version's payload + the kart.list payload in one
	// round-trip so the client doesn't pay two SSH handshakes per circuit.
	MethodServerStatus = "server.status"

	MethodKartNew         = "kart.new"
	MethodKartStart       = "kart.start"
	MethodKartStop        = "kart.stop"
	MethodKartRestart     = "kart.restart"
	MethodKartRecreate    = "kart.recreate"
	MethodKartRebuild     = "kart.rebuild"
	MethodKartDriftCheck  = "kart.drift_check"
	MethodKartDelete      = "kart.delete"
	MethodKartList        = "kart.list"
	MethodKartInfo        = "kart.info"
	MethodKartEnable      = "kart.enable"
	MethodKartDisable     = "kart.disable"
	MethodKartLogs        = "kart.logs"
	MethodKartSessionEnv  = "kart.session_env"
	MethodKartMigrateList = "kart.migrate_list"
	MethodKartConnect     = "kart.connect"
	MethodKartProbePorts  = "kart.probe_ports"

	MethodCharacterNew     = "character.new"
	MethodCharacterPatch   = "character.patch"
	MethodCharacterReplace = "character.replace"
	MethodCharacterList    = "character.list"
	MethodCharacterShow    = "character.show"
	MethodCharacterRemove  = "character.remove"

	MethodChestNew    = "chest.new"
	MethodChestPatch  = "chest.patch"
	MethodChestGet    = "chest.get"
	MethodChestList   = "chest.list"
	MethodChestRemove = "chest.remove"

	MethodTuneNew     = "tune.new"
	MethodTunePatch   = "tune.patch"
	MethodTuneReplace = "tune.replace"
	MethodTuneList    = "tune.list"
	MethodTuneShow    = "tune.show"
	MethodTuneRemove  = "tune.remove"

	MethodConfigShow = "config.show"
	MethodConfigSet  = "config.set"

	MethodCircuitBrowseStart = "circuit.browse_start"
	MethodCircuitBrowseStop  = "circuit.browse_stop"

	MethodSkillList    = "skill.list"
	MethodSkillResolve = "skill.resolve"

	MethodRunList    = "run.list"
	MethodRunResolve = "run.resolve"
)

// Methods returns the catalog in source order. Keep in sync with the
// const block — the cost of duplicating the names is paid here so
// consumers (cliscript, clihelp, docs) don't have to grep.
func Methods() []string {
	return []string{
		MethodServerVersion, MethodServerInfo, MethodServerInit, MethodServerVerify,
		MethodServerStatus,
		MethodKartNew, MethodKartStart, MethodKartStop, MethodKartRestart,
		MethodKartRecreate, MethodKartRebuild, MethodKartDriftCheck,
		MethodKartDelete, MethodKartList, MethodKartInfo,
		MethodKartEnable, MethodKartDisable, MethodKartLogs,
		MethodKartSessionEnv,
		MethodKartMigrateList,
		MethodKartConnect, MethodKartProbePorts,
		MethodCharacterNew, MethodCharacterPatch, MethodCharacterReplace,
		MethodCharacterList, MethodCharacterShow, MethodCharacterRemove,
		MethodChestNew, MethodChestPatch, MethodChestGet, MethodChestList, MethodChestRemove,
		MethodTuneNew, MethodTunePatch, MethodTuneReplace,
		MethodTuneList, MethodTuneShow, MethodTuneRemove,
		MethodConfigShow, MethodConfigSet,
		MethodCircuitBrowseStart, MethodCircuitBrowseStop,
		MethodSkillList, MethodSkillResolve,
		MethodRunList, MethodRunResolve,
	}
}
