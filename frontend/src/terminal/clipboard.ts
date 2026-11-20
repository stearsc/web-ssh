// 复制只操作客户端选区，不接触终端输入或连接状态。
export async function copyText(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    // 异步拒绝原样上报，不能再走另一条路径重复复制。
    await navigator.clipboard.writeText(text)
    return
  }
  const previous = document.activeElement
  const field = document.createElement('textarea')
  field.value = text
  field.readOnly = true
  field.setAttribute('aria-label', '临时复制文本')
  field.style.cssText = 'position:fixed;left:-10000px;top:0;opacity:0'
  document.body.appendChild(field)
  try {
    field.select()
    if (!document.execCommand?.('copy'))
      throw new Error('浏览器未允许复制，请使用右键复制')
  } finally {
    field.remove()
    if (previous instanceof HTMLElement && previous.isConnected)
      previous.focus({ preventScroll: true })
  }
}

// 返回 false 只阻止 xterm；浏览器默认动作需要额外显式取消。
// 浏览器在事件到达页面之前处理的保留快捷键不在页面控制范围内。
export function clipboardKeyHandler(isMac: boolean, copy: () => void) {
  return (event: KeyboardEvent): boolean => {
    if (
      isMac ||
      !event.ctrlKey ||
      !event.shiftKey ||
      event.altKey ||
      event.metaKey ||
      event.key.toLowerCase() !== 'c'
    )
      return true
    event.preventDefault()
    event.stopPropagation()
    if (event.type === 'keydown' && !event.repeat) copy()
    return false
  }
}
