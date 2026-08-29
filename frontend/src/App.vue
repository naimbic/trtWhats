<script setup lang="ts">
import { computed } from 'vue'
import { RouterView } from 'vue-router'
import { Toaster } from 'vue-sonner'
import { ConfigProvider } from 'reka-ui'
import { useI18n } from 'vue-i18n'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useColorMode } from '@/composables/useColorMode'
import { isRTL } from '@/i18n'

// Initialize color mode early
const { colorMode } = useColorMode()

// TRT custom patch #34: drive text direction from the active locale so RTL
// locales (Arabic) mirror the whole UI. ConfigProvider makes every reka-ui
// component (ScrollArea, Popover, dropdowns, ...) direction-aware.
const { locale } = useI18n()
const dir = computed<'ltr' | 'rtl'>(() => (isRTL(locale.value) ? 'rtl' : 'ltr'))
</script>

<template>
  <ConfigProvider :dir="dir">
    <TooltipProvider>
      <div class="min-h-screen bg-background font-sans antialiased">
        <RouterView />
        <Toaster position="top-right" richColors :theme="colorMode" offset="72px" />
      </div>
    </TooltipProvider>
  </ConfigProvider>
</template>
