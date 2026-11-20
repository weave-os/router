import Anthropic from '@anthropic-ai/sdk';
import * as vscode from 'vscode';
import { estimateTokens, toMessagesInput } from './messages';

const vendor = 'weave-router';
const connectCommand = 'weaveRouter.connect';
const disconnectCommand = 'weaveRouter.disconnect';
const setRouterUrlCommand = 'weaveRouter.setRouterUrl';
const baseUrlSetting = 'weaveRouter.baseUrl';
const hostedRouterUrl = 'https://router.weaveos.com';
const routerKeyHeader = 'X-Weave-Router-Key';
const model = {
  id: 'auto',
  name: 'Weave Router',
  family: 'weave-router',
  version: '1',
  maxInputTokens: 123_904,
  maxOutputTokens: 4_096,
  capabilities: { toolCalling: true, imageInput: false },
} satisfies vscode.LanguageModelChatInformation;

function routerBaseUrl(): string {
  return normalizeRouterUrl(vscode.workspace.getConfiguration('weaveRouter').get('baseUrl', hostedRouterUrl));
}

function normalizeRouterUrl(configuredUrl: string): string {
  let url: URL;
  try {
    url = new URL(configuredUrl);
  } catch {
    throw new Error('Enter a valid Router URL.');
  }
  const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
  if ((url.protocol !== 'https:' && !(loopback && url.protocol === 'http:')) ||
      url.username || url.password || url.search || url.hash) {
    throw new Error('Use an HTTPS Router URL without credentials, a query, or a fragment. HTTP is supported on loopback.');
  }
  return url.href.replace(/\/+$/, '');
}

function secretName(baseUrl: string) {
  return `weaveRouter.key:${baseUrl}`;
}

async function setRouterUrl(): Promise<void> {
  const configuration = vscode.workspace.getConfiguration('weaveRouter');
  const configuredUrl = configuration.get('baseUrl', hostedRouterUrl);
  const selectedUrl = await vscode.window.showInputBox({
    title: 'Weave Router URL',
    prompt: 'Keep the hosted URL or enter your organization’s Router base URL.',
    value: configuredUrl,
    ignoreFocusOut: true,
    validateInput(value) {
      try {
        normalizeRouterUrl(value);
        return undefined;
      } catch (error) {
        return error instanceof Error ? error.message : 'Enter a valid Router URL.';
      }
    },
  });
  if (selectedUrl === undefined) return;
  const baseUrl = normalizeRouterUrl(selectedUrl);
  if (baseUrl !== configuredUrl) {
    await configuration.update('baseUrl', baseUrl, vscode.ConfigurationTarget.Global);
    void vscode.window.showInformationMessage(`Weave Router URL set to ${baseUrl}.`);
  }
}

async function connect(secrets: vscode.SecretStorage): Promise<void> {
  const baseUrl = routerBaseUrl();
  const key = (await vscode.window.showInputBox({
    title: 'Connect Weave Router',
    prompt: `Enter a personal Router key for ${baseUrl}`,
    password: true,
    ignoreFocusOut: true,
    validateInput: value => value.trim().startsWith('rk_') ? undefined : 'Enter a Router key beginning with rk_.',
  }))?.trim();
  if (!key) return;
  const response = await fetch(`${baseUrl}/validate`, {
    headers: { [routerKeyHeader]: key },
    redirect: 'error',
    signal: AbortSignal.timeout(10_000),
  });
  await response.body?.cancel();
  if (!response.ok) throw new Error(`Router key check failed (HTTP ${response.status}). Check the URL and key.`);
  await secrets.store(secretName(baseUrl), key);
  void vscode.window.showInformationMessage('Weave Router is connected. Select Weave Router in the Copilot Chat model picker.');
}

async function streamReply(
  secrets: vscode.SecretStorage,
  history: readonly vscode.LanguageModelChatRequestMessage[],
  options: vscode.ProvideLanguageModelChatResponseOptions,
  progress: vscode.Progress<vscode.LanguageModelResponsePart>,
  cancellation: vscode.CancellationToken,
): Promise<void> {
  const baseUrl = routerBaseUrl();
  const key = await secrets.get(secretName(baseUrl));
  if (!key) throw new Error('Run Weave Router: Connect to configure this Router.');
  const { request, editorToolNames } = toMessagesInput(history, options);
  if (!request.messages.length) throw new Error('Weave Router received no supported chat messages.');
  const controller = new AbortController();
  const cancelListener = cancellation.onCancellationRequested(() => controller.abort());
  if (cancellation.isCancellationRequested) controller.abort();
  try {
    const client = new Anthropic({
      baseURL: baseUrl,
      apiKey: null,
      authToken: null,
      logLevel: 'off',
      maxRetries: 0,
      // Explicitly omit SDK authentication so the key stays a Router credential.
      defaultHeaders: { 'x-api-key': null, [routerKeyHeader]: key },
      fetchOptions: { redirect: 'error' },
    });
    const stream = client.messages.stream({
      ...request,
      model: model.id,
      max_tokens: model.maxOutputTokens,
    }, { signal: controller.signal });
    let completed = false;
    stream.on('streamEvent', event => {
      if (event.type === 'message_stop') completed = true;
    });
    stream.on('text', text => progress.report(new vscode.LanguageModelTextPart(text)));
    stream.on('contentBlock', block => {
      if (block.type !== 'tool_use') return;
      const name = editorToolNames.get(block.name);
      if (!name || typeof block.input !== 'object' || block.input === null || Array.isArray(block.input)) {
        throw new Error('Weave Router returned an invalid tool call.');
      }
      progress.report(new vscode.LanguageModelToolCallPart(block.id, name, block.input));
    });
    await stream.finalMessage();
    if (!completed) throw new Error('Missing message_stop');
  } catch (error) {
    if (cancellation.isCancellationRequested) return;
    if (error instanceof Anthropic.APIError) {
      if (error.status === 401 || error.status === 403) {
        throw new Error(`Router authorization failed (HTTP ${error.status}). Run Weave Router: Connect to check your key.`);
      }
      throw new Error(error.status ? `Weave Router returned HTTP ${error.status}.` : 'Weave Router ended the request with an error.');
    }
    throw new Error('Weave Router returned an invalid or incomplete Messages stream.');
  } finally {
    cancelListener.dispose();
  }
}

export function activate(context: vscode.ExtensionContext): void {
  const modelChanges = new vscode.EventEmitter<void>();
  const provider: vscode.LanguageModelChatProvider = {
    onDidChangeLanguageModelChatInformation: modelChanges.event,
    async provideLanguageModelChatInformation() {
      return (await context.secrets.get(secretName(routerBaseUrl()))) ? [model] : [];
    },
    provideLanguageModelChatResponse(_model, history, options, progress, cancellation) {
      return streamReply(context.secrets, history, options, progress, cancellation);
    },
    async provideTokenCount(_model, message) {
      return estimateTokens(message);
    },
  };
  context.subscriptions.push(
    modelChanges,
    vscode.lm.registerLanguageModelChatProvider(vendor, provider),
    context.secrets.onDidChange(change => {
      if (change.key.startsWith('weaveRouter.key:')) modelChanges.fire();
    }),
    vscode.workspace.onDidChangeConfiguration(change => {
      if (change.affectsConfiguration(baseUrlSetting)) modelChanges.fire();
    }),
    vscode.commands.registerCommand(setRouterUrlCommand, setRouterUrl),
    vscode.commands.registerCommand(connectCommand, async () => {
      try {
        await connect(context.secrets);
      } catch (error) {
        if (error instanceof TypeError || error instanceof DOMException) {
          void vscode.window.showErrorMessage('Could not reach Weave Router to validate your key. Check the Router URL and network.');
        } else if (error instanceof Error) {
          void vscode.window.showErrorMessage(error.message);
        } else {
          void vscode.window.showErrorMessage('Could not connect Weave Router. Try again.');
        }
      }
    }),
    vscode.commands.registerCommand(disconnectCommand, async () => {
      const baseUrl = routerBaseUrl();
      await context.secrets.delete(secretName(baseUrl));
      void vscode.window.showInformationMessage(`Removed the saved Weave Router key for ${baseUrl}.`);
    }),
  );
}
