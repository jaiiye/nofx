import { notify } from './notify'

/**
 * Copy text to the clipboard and show a toast notification.
 *
 * Runs on plain-HTTP dashboards too: `navigator.clipboard` only exists in a
 * secure context (HTTPS or localhost), so a LAN install used to get `undefined`
 * and the copy button silently did nothing. The legacy execCommand path covers
 * that case. Returns whether the copy succeeded so callers can also drive their
 * own UI state.
 */
export async function copyWithToast(text: string, successMsg = 'Copied'): Promise<boolean> {
  if (await copyTextToClipboard(text)) {
    notify.success(successMsg)
    return true
  }
  notify.error(
    'Copy failed — the browser blocked clipboard access. Select the text and press Ctrl/Cmd+C.'
  )
  return false
}

/** copyTextToClipboard tries the async clipboard API, then the legacy path. */
export async function copyTextToClipboard(text: string): Promise<boolean> {
  if (!text) return false

  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Exposed but rejected: fall through to the legacy path.
  }

  return legacyCopy(text)
}

function legacyCopy(text: string): boolean {
  try {
    const el = document.createElement('textarea')
    el.value = text
    el.setAttribute('readonly', '')
    el.style.position = 'fixed'
    el.style.top = '-1000px'
    el.style.opacity = '0'
    document.body.appendChild(el)

    const selection = document.getSelection()
    const previous = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null

    el.select()
    el.setSelectionRange(0, el.value.length)
    const ok = document.execCommand('copy')

    document.body.removeChild(el)
    if (previous && selection) {
      selection.removeAllRanges()
      selection.addRange(previous)
    }
    return ok
  } catch {
    return false
  }
}

export default { copyWithToast, copyTextToClipboard }
