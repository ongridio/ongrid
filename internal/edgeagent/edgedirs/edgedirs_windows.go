//go:build windows

package edgedirs

// Windows 部署目录约定。
//
//   - binary → C:\Program Files\ongrid-edge\bin\
//   - data   → C:\ProgramData\ongrid-edge\
//
// ProgramData 是 Windows Service 标准数据目录，NetworkService 可写。
const (
	BinDir       = `C:\Program Files\ongrid-edge\bin`
	DataDir      = `C:\ProgramData\ongrid-edge`
	PluginWorkDir = `C:\ProgramData\ongrid-edge\plugins`
	StageDir     = `C:\ProgramData\ongrid-edge\upgrade`
)

// HealthFile 是 worker.exe 与 supervisor.exe 之间的 health.json IPC 文件路径。
const HealthFile = DataDir + `\health.json`

// WorkerBinary 是 worker.exe 在 BinDir 下的文件名。
const WorkerBinary = "ongrid-edge-worker.exe"
