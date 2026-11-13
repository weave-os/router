/**
 * Copies the text to the clipboard.
 *
 * @returns Whether the clipboard write succeeded.
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch (error) {
    console.error("Failed to copy to clipboard", error);
    return false;
  }
}
