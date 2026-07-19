# Windows registration engine

This directory vendors the CloakBrowser-based Grok registration worker used by
the grok2api admin Registration page on Windows.

Source lineage:

- `grok-free-register-oss` (engine + setup scripts)
- `grok-windows-bundle` adaptation for `REGISTER_OUTPUT_DIR`

## Managed mode (recommended)

1. Run setup once from this directory:

```powershell
powershell -ExecutionPolicy Bypass -File .\setup.ps1 -SmokeTest
```

2. Start grok2api as usual.
3. Open **Registration** in the admin UI and use the Windows register panel.

When launched by grok2api, the worker writes to:

```text
<data-dir>/windows-register/
  accounts.txt
  grok.txt
```

The Go service injects `REGISTER_OUTPUT_DIR` and related env vars. Do not put
live credentials into git.

## Standalone mode

You can still run the engine manually:

```powershell
.\start.ps1
.\start.ps1 -Target 1
.\start.ps1 -Target 10 -MaxMem 4G -VerboseLog
```

Without `REGISTER_OUTPUT_DIR`, output defaults to `keys\` under this directory.

Custom browser path:

```powershell
$env:CLOAKBROWSER_EXECUTABLE_PATH = "C:\path\to\chrome.exe"
.\start.ps1
```

## Requirements

- Windows 10/11 or Windows Server
- Python 3.10+
- Network access for first-time PyPI / CloakBrowser download

## Security

Account files contain live credentials. Keep `keys\`, `.env`, and
`data/windows-register` local. Never publish them.
