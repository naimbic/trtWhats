<script setup lang="ts">
// TRT custom patch #92: Telegram sale notifications — per-org on/off + bot token + chat id.
// When enabled, every new sale (conversion value saved on a contact) is pushed to
// this Telegram chat.
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Loader2, Send } from 'lucide-vue-next'
import { telegramService } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { toast } from 'vue-sonner'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
const authStore = useAuthStore()
const canWrite = computed(() => authStore.hasPermission('settings.notification', 'write'))
const MASK = '••••••••'

const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const hasBotToken = ref(false)

const form = ref<{ enabled: boolean; chat_id: string; bot_token: string }>({
  enabled: false,
  chat_id: '',
  bot_token: '',
})

async function load() {
  loading.value = true
  try {
    const res = await telegramService.get()
    const s = (res.data as any)?.data || (res.data as any) || {}
    form.value.enabled = !!s.enabled
    form.value.chat_id = s.chat_id || ''
    hasBotToken.value = !!s.has_bot_token
    form.value.bot_token = hasBotToken.value ? MASK : ''
  } catch (e) {
    toast.error(getErrorMessage(e, t('telegram.loadFailed', 'Impossible de charger les réglages Telegram')))
  } finally {
    loading.value = false
  }
}
onMounted(load)

function payload() {
  const p: Record<string, any> = {
    enabled: form.value.enabled,
    chat_id: form.value.chat_id.trim(),
  }
  // Only send the token when the user actually typed a new one (not the mask).
  if (form.value.bot_token && form.value.bot_token !== MASK) {
    p.bot_token = form.value.bot_token.trim()
  }
  return p
}

async function save() {
  saving.value = true
  try {
    await telegramService.update(payload())
    toast.success(t('common.saved', 'Enregistré'))
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('telegram.saveFailed', "Échec de l'enregistrement")))
  } finally {
    saving.value = false
  }
}

async function sendTest() {
  testing.value = true
  try {
    // Persist first so the server tests the current token + chat id.
    await telegramService.update(payload())
    await telegramService.test()
    hasBotToken.value = true
    form.value.bot_token = MASK
    toast.success(t('telegram.testSent', 'Message de test envoyé — vérifiez Telegram'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('telegram.testFailed', 'Échec du test Telegram')))
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <div class="p-4 md:p-6 max-w-2xl mx-auto space-y-4">
    <div class="flex items-center gap-2">
      <Send class="h-5 w-5" />
      <h1 class="text-lg font-semibold">{{ $t('telegram.title', 'Telegram') }}</h1>
    </div>

    <Card>
      <CardHeader class="pb-2">
        <CardTitle class="text-sm">{{ $t('telegram.notifyTitle', 'Notifications de ventes') }}</CardTitle>
      </CardHeader>
      <CardContent class="space-y-4">
        <p class="text-xs text-muted-foreground">
          {{ $t('telegram.desc', 'Quand c’est activé, chaque nouvelle vente (valeur de conversion enregistrée sur un contact) est envoyée dans ce chat Telegram.') }}
        </p>

        <div v-if="loading" class="py-6 flex justify-center">
          <Loader2 class="h-5 w-5 animate-spin text-muted-foreground" />
        </div>

        <template v-else>
          <div class="flex items-center justify-between rounded-md border p-3">
            <div>
              <div class="text-sm font-medium">{{ $t('telegram.enabled', 'Activer les notifications Telegram') }}</div>
              <div class="text-xs text-muted-foreground">{{ $t('telegram.enabledHint', 'Désactivé = aucune notification envoyée') }}</div>
            </div>
            <Switch :checked="form.enabled" :disabled="!canWrite" @update:checked="form.enabled = $event" />
          </div>

          <div class="space-y-1.5">
            <Label class="text-xs">{{ $t('telegram.botToken', 'Bot Token') }}</Label>
            <Input v-model="form.bot_token" type="password" :placeholder="MASK" :disabled="!canWrite" class="font-mono text-xs" />
            <p class="text-xs text-muted-foreground">
              {{ $t('telegram.botTokenHint', 'Créez un bot via @BotFather et collez son token ici.') }}
            </p>
          </div>

          <div class="space-y-1.5">
            <Label class="text-xs">{{ $t('telegram.chatId', 'Chat ID') }}</Label>
            <Input v-model="form.chat_id" :placeholder="'-1001234567890'" :disabled="!canWrite" class="font-mono text-xs" />
            <p class="text-xs text-muted-foreground">
              {{ $t('telegram.chatIdHint', 'ID du chat ou canal cible. Ajoutez le bot au groupe, puis récupérez le chat_id.') }}
            </p>
          </div>

          <div v-if="canWrite" class="flex items-center gap-2 pt-1">
            <Button :disabled="saving" @click="save">
              <Loader2 v-if="saving" class="h-4 w-4 mr-2 animate-spin" />
              {{ $t('common.save', 'Enregistrer') }}
            </Button>
            <Button variant="outline" :disabled="testing" @click="sendTest">
              <Loader2 v-if="testing" class="h-4 w-4 mr-2 animate-spin" />
              {{ $t('telegram.test', 'Envoyer un test') }}
            </Button>
          </div>
        </template>
      </CardContent>
    </Card>
  </div>
</template>
