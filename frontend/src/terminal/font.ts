import { reactive, readonly } from 'vue'

export const FONT_STORAGE_KEY = 'web-ssh.terminal-font.v1'
export const FONT_MIN = 10
export const FONT_MAX = 24
export const FONT_FAMILIES = [
  { id: 'system', label: '系统默认等宽字体', css: 'monospace' },
  { id: 'menlo', label: 'Menlo', css: 'Menlo, monospace' },
  { id: 'monaco', label: 'Monaco', css: 'Monaco, monospace' },
  { id: 'sf-mono', label: 'SF Mono', css: '"SF Mono", Menlo, monospace' },
  { id: 'consolas', label: 'Consolas', css: 'Consolas, monospace' },
  {
    id: 'liberation-mono',
    label: 'Liberation Mono',
    css: '"Liberation Mono", monospace',
  },
] as const
export type FontFamily = (typeof FONT_FAMILIES)[number]['id']
export interface TerminalFont {
  size: number
  family: FontFamily
}
const defaults = (): TerminalFont => ({ size: 14, family: 'system' })

export function readTerminalFont(): TerminalFont {
  try {
    const raw = localStorage.getItem(FONT_STORAGE_KEY)
    if (!raw) return defaults()
    const value: unknown = JSON.parse(raw)
    if (!value || typeof value !== 'object') return defaults()
    const { size, family } = value as Partial<TerminalFont>
    const selected = FONT_FAMILIES.find((item) => item.id === family)
    if (
      typeof size !== 'number' ||
      !Number.isInteger(size) ||
      size < FONT_MIN ||
      size > FONT_MAX ||
      !selected
    )
      return defaults()
    return { size, family: selected.id }
  } catch {
    // 浏览器禁止存储或数据损坏时，使用本地默认值，不影响终端创建。
    return defaults()
  }
}
const settings = reactive(readTerminalFont())
export const terminalFont = readonly(settings)
export function setTerminalFont(size: number, family: string): void {
  if (!Number.isInteger(size) || size < FONT_MIN || size > FONT_MAX) return
  const selected = FONT_FAMILIES.find((item) => item.id === family)
  if (!selected) return
  settings.size = size
  settings.family = selected.id
  try {
    localStorage.setItem(FONT_STORAGE_KEY, JSON.stringify(settings))
  } catch {
    // 存储不可用时，本页所有终端仍即时使用本次设置。
  }
}
export function terminalFontFamily(): string {
  return FONT_FAMILIES.find((item) => item.id === settings.family)!.css
}
