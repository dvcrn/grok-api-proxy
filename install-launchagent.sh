#!/bin/bash

set -e

# Get the absolute path of the current directory
PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
PLIST_NAME="sh.d.grok-oauth-proxy.plist"
PLIST_LOCAL="${PROJECT_DIR}/${PLIST_NAME}"
LAUNCHAGENTS_DIR="${HOME}/Library/LaunchAgents"
PLIST_SYMLINK="${LAUNCHAGENTS_DIR}/${PLIST_NAME}"

echo "Installing grok-oauth-proxy LaunchAgent..."
echo "Project directory: ${PROJECT_DIR}"

# Create LaunchAgents directory if it doesn't exist
mkdir -p "${LAUNCHAGENTS_DIR}"

# Check if Go is available and decide on execution method
GO_BIN="$(which go 2>/dev/null || echo "")"
if [ -n "${GO_BIN}" ]; then
    echo "Go is installed at: ${GO_BIN}"
    echo "Building binary..."

    # Build the binary
    cd "${PROJECT_DIR}"
    if command -v mise &> /dev/null; then
        mise run build
    else
        go build -o ./bin/proxy .
    fi

    if [ -f "${PROJECT_DIR}/bin/proxy" ] && [ -x "${PROJECT_DIR}/bin/proxy" ]; then
        echo "Binary built successfully"
        BINARY_PATH="${PROJECT_DIR}/bin/proxy"
        USE_GO_RUN="false"
    else
        echo "Binary build failed or not executable, will use 'go run' instead"
        USE_GO_RUN="true"
    fi
else
    echo "Go not found, LaunchAgent will use 'go run' (requires Go at runtime)"
    USE_GO_RUN="true"
    GO_BIN="go"
fi

# Generate the plist file locally in project directory
if [ "${USE_GO_RUN}" = "true" ]; then
    cat > "${PLIST_LOCAL}" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>sh.d.grok-oauth-proxy</string>

    <key>ProgramArguments</key>
    <array>
        <string>${GO_BIN}</string>
        <string>run</string>
        <string>${PROJECT_DIR}/main.go</string>
    </array>

    <key>WorkingDirectory</key>
    <string>${PROJECT_DIR}</string>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
        <key>Crashed</key>
        <true/>
    </dict>

    <key>ThrottleInterval</key>
    <integer>30</integer>

    <key>StandardOutPath</key>
    <string>${HOME}/Library/Logs/grok-oauth-proxy.log</string>

    <key>StandardErrorPath</key>
    <string>${HOME}/Library/Logs/grok-oauth-proxy.error.log</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
        <key>HOME</key>
        <string>${HOME}</string>
    </dict>
</dict>
</plist>
EOF
else
    cat > "${PLIST_LOCAL}" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>sh.d.grok-oauth-proxy</string>

    <key>ProgramArguments</key>
    <array>
        <string>${BINARY_PATH}</string>
    </array>

    <key>WorkingDirectory</key>
    <string>${PROJECT_DIR}</string>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
        <key>Crashed</key>
        <true/>
    </dict>

    <key>ThrottleInterval</key>
    <integer>30</integer>

    <key>StandardOutPath</key>
    <string>${HOME}/Library/Logs/grok-oauth-proxy.log</string>

    <key>StandardErrorPath</key>
    <string>${HOME}/Library/Logs/grok-oauth-proxy.error.log</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>${HOME}</string>
    </dict>
</dict>
</plist>
EOF
fi

echo "Plist file created at: ${PLIST_LOCAL}"

if [ ! -f "${PLIST_LOCAL}" ]; then
    echo "❌ Error: Failed to create plist file"
    exit 1
fi

if [ -L "${PLIST_SYMLINK}" ]; then
    echo "Removing old symlink..."
    rm "${PLIST_SYMLINK}"
fi

if launchctl list | grep -q "sh.d.grok-oauth-proxy"; then
    echo "Unloading existing service..."
    launchctl unload "${PLIST_SYMLINK}" 2>/dev/null || true
fi

echo "Creating symlink: ${PLIST_SYMLINK} -> ${PLIST_LOCAL}"
ln -sf "${PLIST_LOCAL}" "${PLIST_SYMLINK}"

if [ ! -L "${PLIST_SYMLINK}" ]; then
    echo "❌ Error: Failed to create symlink"
    exit 1
fi

echo "Loading service..."
launchctl load "${PLIST_SYMLINK}"

sleep 2
if launchctl list | grep -q "sh.d.grok-oauth-proxy"; then
    echo "✅ LaunchAgent installed and started successfully!"
    echo ""
    echo "Service management commands:"
    echo "  Check status:  launchctl list | grep grok-oauth-proxy"
    echo "  View logs:     tail -f ~/Library/Logs/grok-oauth-proxy.log"
    echo "  View errors:   tail -f ~/Library/Logs/grok-oauth-proxy.error.log"
    echo "  Stop service:  launchctl unload ~/Library/LaunchAgents/${PLIST_NAME}"
    echo "  Start service: launchctl load ~/Library/LaunchAgents/${PLIST_NAME}"
    echo "  Uninstall:     ./uninstall-launchagent.sh"
else
    echo "⚠️  Service may not have started correctly. Check logs at:"
    echo "  ~/Library/Logs/grok-oauth-proxy.error.log"
fi
