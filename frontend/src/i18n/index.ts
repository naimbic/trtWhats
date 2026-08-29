import { createI18n } from 'vue-i18n'
import en from './locales/en.json'

export type MessageSchema = typeof en

// Auto-discover available locales from the locales folder
// Vite imports all JSON files at build time
// Using import: 'default' to get JSON content directly (required for Vite 5+)
const localeModules = import.meta.glob('./locales/*.json', { eager: true, import: 'default' }) as Record<string, MessageSchema>

// Supported locales for trtWhats: English, French, Arabic, Darija (Moroccan).
const localeNames: Record<string, { name: string; nativeName: string }> = {
  en: { name: 'English', nativeName: 'English' },
  fr: { name: 'French', nativeName: 'Français' },
  ar: { name: 'Arabic', nativeName: 'العربية' },
  ary: { name: 'Darija', nativeName: 'الدارجة' },
}

// Right-to-left locales. When one is active the whole UI is mirrored
// (sidebar/menu moves to the right, chat list + messages align right).
// 'ary' = Moroccan Darija, written in Arabic script -> RTL.
const RTL_LOCALES = new Set(['ar', 'ary', 'he', 'fa', 'ur'])

export function isRTL(locale: string): boolean {
  return RTL_LOCALES.has(locale)
}

// Apply text direction to the document for the given locale.
export function applyDir(locale: string) {
  const dir = isRTL(locale) ? 'rtl' : 'ltr'
  document.documentElement.setAttribute('dir', dir)
  document.documentElement.setAttribute('lang', locale)
}

// Auto-generate SUPPORTED_LOCALES from available files
export const SUPPORTED_LOCALES = Object.keys(localeModules).map(path => {
  const code = path.replace('./locales/', '').replace('.json', '')
  const names = localeNames[code] || { name: code, nativeName: code }
  return { code, ...names }
})

export type SupportedLocale = string

// Build messages object from all locale files
const messages: Record<string, MessageSchema> = {}
for (const path in localeModules) {
  const code = path.replace('./locales/', '').replace('.json', '')
  messages[code] = localeModules[path]
}

// Get saved locale or detect from browser
function getDefaultLocale(): string {
  // Check localStorage first
  const saved = localStorage.getItem('locale')
  if (saved && messages[saved]) {
    return saved
  }

  // Detect from browser
  const browserLang = navigator.language.split('-')[0]
  if (messages[browserLang]) {
    return browserLang
  }

  return 'en'
}

export const i18n = createI18n({
  legacy: false, // Use Composition API
  locale: getDefaultLocale(),
  fallbackLocale: 'en',
  messages,
})

// Apply the initial text direction (RTL for Arabic) on load.
applyDir(i18n.global.locale.value)

// Helper to change locale
export function setLocale(locale: string) {
  if (!messages[locale]) {
    console.warn(`Locale '${locale}' not available`)
    return
  }
  i18n.global.locale.value = locale
  localStorage.setItem('locale', locale)
  applyDir(locale)
}

// Get current locale
export function getLocale(): string {
  return i18n.global.locale.value
}
