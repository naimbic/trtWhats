<script setup lang="ts">
// TRT custom patch #92: Telegram sale notifications — per-org on/off + bot token + chat id.
// When enabled, every new sale (conversion value saved on a contact) is pushed to
// this Telegram chat. Built with the app's native settings-panel markup so it
// renders identically to the rest of Settings in both themes + RTL.
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Loader2, Send } from 'lucide-vue-next'
import { telegramService } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { toast } from 'vue-sonner'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
const authStore = useAuthStore()
const canWrite = computed(() => authStore.hasPermission('settings.general', 'write'))
const MASK = '••••••••'

const loading = ref(true)
const saving = ref(false)
const testing = ref(false)
const hasBotToken = ref(false)

const enabled = ref(false)
const chatId = ref('')
const botToken = ref('')

async function load() {
  loading.value = true
  try {
    const res = await telegramService.get()
    const s: any = (res.data as any)?.data ?? (res.data as any) ?? {}
    enabled.value = !!s.enabled
    chatId.value = s.chat_id || ''
    hasBotToken.value = !!s.has_bot_token
    botToken.value = hasBotToken.value ? MASK : ''
  } catch (e) {
    // A fresh org has no row yet — that's fine, start with defaults.
    toast.error(getErrorMessage(e, 'Impossible de charger les réglages Telegram'))
  } finally {
    loading.value = false
  }
}
onMounted(load)

function payload() {
  const p: Record<string, any> = { enabled: enabled.value, chat_id: chatId.value.trim() }
  if (botToken.value && botToken.value !== MASK) p.bot_token = botToken.value.trim()
  return p
}

async function save() {
  saving.value = true
  try {
    await telegramService.update(payload())
    toast.success(t('common.saved', 'Enregistré'))
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, "Échec de l'enregistrement"))
  } finally {
    saving.value = false
  }
}

async function sendTest() {
  testing.value = true
  try {
    await telegramService.update(payload()) // persist first so the server tests the current values
    await telegramService.test()
    hasBotToken.value = true
    botToken.value = MASK
    toast.success('Message de test envoyé — vérifiez Telegram')
  } catch (e) {
    toast.error(getErrorMessage(e, 'Échec du test Telegram'))
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50 overflow-y-auto">
    <div class="p-6 space-y-4 max-w-2xl mx-auto w-full">
      <!-- Header -->
      <div class="flex items-center gap-2">
        <Send class="h-5 w-5 text-white/80 light:text-gray-700" />
        <h1 class="text-lg font-semibold text-white light:text-gray-900">Telegram</h1>
      </div>

      <!-- Panel -->
      <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
        <div class="p-6 pb-3">
          <h3 class="text-base font-semibold text-white light:text-gray-900">Notifications de ventes</h3>
          <p class="text-sm text-white/40 light:text-gray-500">
            Quand c'est activé, chaque nouvelle vente (valeur de conversion enregistrée sur un contact) est envoyée dans ce chat Telegram.
          </p>
        </div>

        <div class="p-6 pt-3 space-y-5">
          <!-- On/off -->
          <div class="flex items-center justify-between gap-4 rounded-lg border border-white/[0.08] bg-white/[0.02] light:bg-gray-50 light:border-gray-200 p-4">
            <div class="min-w-0">
              <div class="text-sm font-medium text-white light:text-gray-900">Activer les notifications Telegram</div>
              <div class="text-xs text-white/40 light:text-gray-500">Désactivé = aucune notification n'est envoyée.</div>
            </div>
            <Switch :checked="enabled" :disabled="!canWrite || loading" @update:checked="enabled = $event" />
          </div>

          <!-- Bot token -->
          <div class="space-y-2">
            <label class="text-sm text-white/70 light:text-gray-700">Bot Token</label>
            <Input
              v-model="botToken"
              type="password"
              :placeholder="MASK"
              :disabled="!canWrite || loading"
              class="font-mono text-xs bg-white/[0.04] border-white/[0.1] text-white/80 light:bg-white light:border-gray-200 light:text-gray-800"
            />
            <p class="text-xs text-white/40 light:text-gray-500">Créez un bot via @BotFather et collez son token ici.</p>
          </div>

          <!-- Chat id -->
          <div class="space-y-2">
            <label class="text-sm text-white/70 light:text-gray-700">Chat ID</label>
            <Input
              v-model="chatId"
              placeholder="-1001234567890"
              :disabled="!canWrite || loading"
              class="font-mono text-xs bg-white/[0.04] border-white/[0.1] text-white/80 light:bg-white light:border-gray-200 light:text-gray-800"
            />
            <p class="text-xs text-white/40 light:text-gray-500">ID du chat ou du canal cible. Ajoutez le bot au groupe, puis récupérez le chat_id.</p>
          </div>

          <!-- Actions -->
          <div class="flex items-center gap-2 pt-1">
            <Button :disabled="saving || !canWrite || loading" @click="save">
              <Loader2 v-if="saving" class="h-4 w-4 mr-2 animate-spin" />
              {{ t('common.save', 'Enregistrer') }}
            </Button>
            <Button variant="outline" :disabled="testing || !canWrite || loading" @click="sendTest">
              <Loader2 v-if="testing" class="h-4 w-4 mr-2 animate-spin" />
              Envoyer un test
            </Button>
            <Loader2 v-if="loading" class="h-4 w-4 animate-spin text-white/40 light:text-gray-400" />
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
