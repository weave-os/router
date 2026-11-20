# Weave Router for VS Code

Use your personal Weave Router account from the GitHub Copilot Chat model picker.

## Connect

1. Install the extension and run **Weave Router: Connect** from the Command Palette.
2. Enter your personal Router key. For a self-hosted Router, set **Weave Router: Base Url** first.
3. Open a new Copilot Chat and select **Weave Router**. If the model does not appear, check `chat.agentHost.byokModels.enabled` and your organization's Copilot BYOK policy.

The extension sends Messages requests to the selected Router. It stores the key in VS Code's secret storage for that Router URL. Run **Weave Router: Connect** again to replace the saved key, or **Weave Router: Disconnect** to remove it from this editor. You can revoke the key in Weave.

The Copilot harness can reject a turn before it reaches the Router when system instructions and tool definitions use up the context window. If that happens, turn off optional tool groups under **Chat: Open Customizations → Tools** and retry in a new chat.

## Develop

Use Node 22 or later. Run `npm ci`, `npm run typecheck`, and `npm run package` in this directory. Install the generated `.vsix` through **Extensions: Install from VSIX** in VS Code 1.146 or later.
