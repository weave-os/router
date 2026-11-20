import { createHash } from 'node:crypto';
import type Anthropic from '@anthropic-ai/sdk';
import * as vscode from 'vscode';

// Agent Host may pass the system role even though the public enum exposes only user and assistant.
const SystemRole = 3;

type MessagesInput = Pick<Anthropic.MessageStreamParams, 'messages' | 'system' | 'tools' | 'tool_choice'>;

function wireToolName(name: string): string {
  if (/^[a-zA-Z0-9_-]{1,64}$/.test(name)) return name;
  const suffix = createHash('sha256').update(name).digest('hex').slice(0, 16);
  return `${name.replace(/[^a-zA-Z0-9_-]/g, '_').slice(0, 47)}_${suffix}`;
}

function textPart(part: unknown): string | undefined {
  if (part instanceof vscode.LanguageModelTextPart) return part.value;
  if (part instanceof vscode.LanguageModelDataPart &&
      (part.mimeType.startsWith('text/') || part.mimeType === 'application/json')) {
    return Buffer.from(part.data).toString('utf8');
  }
  return undefined;
}

function messageBlocks(content: readonly unknown[]): Anthropic.ContentBlockParam[] {
  const blocks: Anthropic.ContentBlockParam[] = [];
  for (const part of content) {
    const text = textPart(part);
    if (text) {
      blocks.push({ type: 'text', text });
    } else if (part instanceof vscode.LanguageModelToolCallPart) {
      blocks.push({ type: 'tool_use', id: part.callId, name: wireToolName(part.name), input: part.input });
    } else if (part instanceof vscode.LanguageModelToolResultPart) {
      const toolText = part.content.map(item => {
        const text = textPart(item);
        if (text !== undefined) return text;
        if (item instanceof vscode.LanguageModelPromptTsxPart) return JSON.stringify(item.value);
        throw new Error('Weave Router received a tool result with unsupported content.');
      }).join('\n');
      blocks.push({ type: 'tool_result', tool_use_id: part.callId, content: toolText });
    } else if (part instanceof vscode.LanguageModelDataPart && part.mimeType.startsWith('image/')) {
      throw new Error('Weave Router for VS Code currently supports text and tools. Remove the image and try again.');
    }
  }
  return blocks;
}

export function toMessagesInput(
  history: readonly vscode.LanguageModelChatRequestMessage[],
  options: vscode.ProvideLanguageModelChatResponseOptions,
): { request: MessagesInput; editorToolNames: Map<string, string> } {
  const messages: { role: Anthropic.MessageParam['role']; content: Anthropic.ContentBlockParam[] }[] = [];
  const system: Anthropic.TextBlockParam[] = [];
  const editorToolNames = new Map<string, string>();
  for (const message of history) {
    if ((message.role as number) === SystemRole) {
      for (const part of message.content) {
        const text = textPart(part);
        if (text) system.push({ type: 'text', text });
      }
      continue;
    }
    const content = messageBlocks(message.content);
    if (!content.length) continue;
    const role = message.role === vscode.LanguageModelChatMessageRole.Assistant ? 'assistant' : 'user';
    const previous = messages.at(-1);
    if (previous?.role === role) previous.content.push(...content);
    else messages.push({ role, content });
  }
  for (const message of messages) {
    // Messages requires tool results to lead the user turn that follows a tool call.
    if (message.role === 'user') {
      message.content = [
        ...message.content.filter(block => block.type === 'tool_result'),
        ...message.content.filter(block => block.type !== 'tool_result'),
      ];
    }
  }
  const tools = options.tools?.map((tool): Anthropic.Tool => {
    const name = wireToolName(tool.name);
    if (editorToolNames.has(name) && editorToolNames.get(name) !== tool.name) {
      throw new Error('Weave Router received conflicting tool names.');
    }
    editorToolNames.set(name, tool.name);
    return { name, description: tool.description, input_schema: { ...tool.inputSchema, type: 'object' } };
  });
  return {
    request: {
      messages,
      system: system.length ? system : undefined,
      tools: tools?.length ? tools : undefined,
      tool_choice: tools?.length && options.toolMode === vscode.LanguageModelChatToolMode.Required
        ? { type: 'any' }
        : undefined,
    },
    editorToolNames,
  };
}

export function estimateTokens(message: string | vscode.LanguageModelChatRequestMessage): number {
  // Auto can route to several tokenizers. Use the wire-sized text and tool arguments for an estimate.
  const contents = typeof message === 'string' ? message : JSON.stringify(messageBlocks(message.content));
  return Math.ceil(Buffer.byteLength(contents) / 3);
}
