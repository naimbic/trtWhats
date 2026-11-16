<script setup lang="ts">
// TRT custom patch #75: connect & manage Facebook Messenger (Page) accounts.
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Loader2, Plus, Trash2, MessageCircle } from 'lucide-vue-next'
import { messengerService, type MessengerAccount } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { toast } from 'vue-sonner'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
const authStore = useAuthStore()
const canWrite = computed(() => authStore.hasPermission('accounts', 'write'))
const MASK = '••••••••'
const webhookUrl = `${window.location.origin}/api/messenger/webhook`

const accounts = ref<MessengerAccount[]>([])
const loading = ref(false)
const showDialog = ref(false)
const saving = ref(false)
const editingId = ref<string | null>(null)
const form = ref<Record<string, any>>({
  name: '', page_id: '', page_name: '',
  webhook_verify_token: '', access_token: '', app_secret: '', is_active: true,
})

async function load() {
  loading.value = true
  try {
    const res = await messengerService.list()
    accounts.value = (res.data as any)?.data?.accounts || (res.data as any)?.accounts || []
  } catch (e) {
    toast.error(getErrorMessage(e, t('messenger.loadFailed', 'Impossible de charger les pages Messenger')))
  } finally {
    loading.value = false
  }
}
onMounted(load)

function openCreate() {
  editingId.value = null
  form.value = {
    name: '', page_id: '', page_name: '',
    webhook_verify_token: 'trtwhats-' + Math.random().toString(36).slice(2, 10),
    access_token: '', app_secret: '', is_active: true,
  }
  showDialog.value = true
}
function openEdit(acc: MessengerAccount) {
  editingId.value = acc.id
  form.value = {
    name: acc.name, page_id: acc.page_id, page_name: acc.page_name,
    webhook_verify_token: acc.webhook_verify_token,
    access_token: acc.has_access_token ? MASK : '',
    app_secret: acc.has_app_secret ? MASK : '',
    is_active: acc.is_active,
  }
  showDialog.value = true
}

async function save() {
  if (!form.value.name?.trim() || !form.value.page_id?.trim()) {
    toast.error(t('messenger.nameIdRequired', 'Nom et Page ID sont requis'))
    return
  }
  saving.value = true
  try {
    const payload: Record<string, any> = {
      name: form.value.name.trim(),
      page_id: form.value.page_id.trim(),
      page_name: form.value.page_name.trim(),
      webhook_verify_token: form.value.webhook_verify_token.trim(),
      is_active: form.value.is_active,
    }
    if (form.value.access_token && form.value.access_token !== MASK) payload.access_token = form.value.access_token
    if (form.value.app_secret && form.value.app_secret !== MASK) payload.app_secret = form.value.app_secret
    if (editingId.value) await messengerService.update(editingId.value, payload)
    else await messengerService.create(payload)
    toast.success(t('common.saved', 'Enregistré'))
    showDialog.value = false
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('messenger.saveFailed', "Échec de l'enregistrement")))
  } finally {
    saving.value = false
  }
}

async function remove(acc: MessengerAccount) {
  if (!confirm(t('messenger.confirmDelete', 'Supprimer cette page Messenger ?'))) return
  try {
    await messengerService.remove(acc.id)
    await load()
    toast.success(t('common.deleted', 'Supprimé'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('messenger.deleteFailed', 'Échec de la suppression')))
  }
}

function copyWebhook() {
  navigator.clipboard?.writeText(webhookUrl).then(() => toast.success(t('common.copied', 'Copié')))
}
</script>

<template>
  <div class="p-4 md:p-6 max-w-3xl mx-auto space-y-4">
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-2">
        <MessageCircle class="h-5 w-5" />
        <h1 class="text-lg font-semibold">{{ $t('messenger.title', 'Facebook Messenger') }}</h1>
      </div>
      <Button v-if="canWrite" size="sm" @click="openCreate">
        <Plus class="h-4 w-4 mr-1" /> {{ $t('messenger.add', 'Ajouter une page') }}
      </Button>
    </div>

    <!-- Webhook config helper -->
    <Card>
      <CardHeader class="pb-2"><CardTitle class="text-sm">{{ $t('messenger.webhookTitle', 'Webhook (à configurer sur Meta)') }}</CardTitle></CardHeader>
      <CardContent class="space-y-2 text-sm">
        <div class="flex items-center gap-2">
          <Input :model-value="webhookUrl" readonly class="font-mono text-xs" />
          <Button size="sm" variant="outline" @click="copyWebhook">{{ $t('common.copy', 'Copier') }}</Button>
        </div>
        <p class="text-xs text-muted-foreground">
          {{ $t('messenger.webhookHint', 'Dans Meta : objet « page », champ « messages ». Utilisez le Verify Token de la page ci-dessous.') }}
        </p>
      </CardContent>
    </Card>

    <div v-if="loading" class="py-8 flex justify-center"><Loader2 class="h-5 w-5 animate-spin text-muted-foreground" /></div>
    <div v-else-if="accounts.length === 0" class="py-8 text-center text-sm text-muted-foreground">
      {{ $t('messenger.empty', 'Aucune page Messenger connectée.') }}
    </div>
    <div v-else class="space-y-2">
      <Card v-for="acc in accounts" :key="acc.id">
        <CardContent class="flex items-center justify-between py-3">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <span class="font-medium truncate">{{ acc.name }}</span>
              <span :class="['text-[10px] px-1.5 py-0.5 rounded-full', acc.is_active ? 'bg-emerald-500/15 text-emerald-600' : 'bg-muted text-muted-foreground']">
                {{ acc.is_active ? $t('common.active', 'Actif') : $t('common.inactive', 'Inactif') }}
              </span>
            </div>
            <p class="text-xs text-muted-foreground truncate">
              Page ID: {{ acc.page_id }}{{ acc.page_name ? ' · ' + acc.page_name : '' }}
              {{ acc.has_access_token ? '' : ' · ' + $t('messenger.noToken', 'jeton manquant') }}
            </p>
          </div>
          <div v-if="canWrite" class="flex items-center gap-2 shrink-0">
            <Button size="sm" variant="outline" @click="openEdit(acc)">{{ $t('common.edit', 'Modifier') }}</Button>
            <Button size="icon" variant="ghost" class="text-destructive" @click="remove(acc)"><Trash2 class="h-4 w-4" /></Button>
          </div>
        </CardContent>
      </Card>
    </div>

    <!-- Create / edit dialog -->
    <Dialog v-model:open="showDialog">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{{ editingId ? $t('messenger.editTitle', 'Modifier la page') : $t('messenger.add', 'Ajouter une page') }}</DialogTitle>
          <DialogDescription>{{ $t('messenger.formDesc', 'Connectez une Page Facebook pour recevoir et répondre aux messages Messenger.') }}</DialogDescription>
        </DialogHeader>
        <div class="space-y-3 py-2">
          <div class="space-y-1.5">
            <Label class="text-xs">{{ $t('messenger.name', 'Nom (référence)') }}</Label>
            <Input v-model="form.name" placeholder="ex: Belle Tulipe FB" />
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div class="space-y-1.5">
              <Label class="text-xs">Page ID</Label>
              <Input v-model="form.page_id" placeholder="Facebook Page ID" />
            </div>
            <div class="space-y-1.5">
              <Label class="text-xs">{{ $t('messenger.pageName', 'Nom de la page') }}</Label>
              <Input v-model="form.page_name" placeholder="Belle Tulipe" />
            </div>
          </div>
          <div class="space-y-1.5">
            <Label class="text-xs">Page Access Token</Label>
            <Input v-model="form.access_token" type="password" :placeholder="MASK" />
          </div>
          <div class="space-y-1.5">
            <Label class="text-xs">App Secret ({{ $t('messenger.optional', 'optionnel, pour vérifier le webhook') }})</Label>
            <Input v-model="form.app_secret" type="password" :placeholder="MASK" />
          </div>
          <div class="space-y-1.5">
            <Label class="text-xs">Webhook Verify Token</Label>
            <Input v-model="form.webhook_verify_token" class="font-mono text-xs" />
          </div>
          <div class="flex items-center gap-2">
            <Switch :checked="form.is_active" @update:checked="form.is_active = $event" />
            <Label class="text-xs">{{ $t('common.active', 'Actif') }}</Label>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" @click="showDialog = false">{{ $t('common.cancel', 'Annuler') }}</Button>
          <Button :disabled="saving" @click="save">
            <Loader2 v-if="saving" class="h-4 w-4 mr-2 animate-spin" />
            {{ $t('common.save', 'Enregistrer') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
