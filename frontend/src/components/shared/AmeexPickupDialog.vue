<script setup lang="ts">
// TRT custom patch #67: global Ameex pickup request ("Demande de ramassage").
// Self-contained: resolves the org's Ameex-enabled number, loads its registered
// pickup addresses, and lets the user pick a Business (address auto-fetched) + a
// pickup time/note. Usable from anywhere (e.g. the sidebar), not just settings.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Loader2 } from 'lucide-vue-next'
import { accountsService } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { toast } from 'vue-sonner'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [v: boolean] }>()
const { t } = useI18n()

interface PickupAddress { id: string; business_id: string; business_name: string; city_id: string; city_name: string; phone: string; address: string }
const accountId = ref('')
const loading = ref(false)
const isSubmitting = ref(false)
const addresses = ref<PickupAddress[]>([])
const selectedBusinessId = ref('')
const noteText = ref('')
const when = ref('') // datetime-local value

const businesses = computed(() => {
  const seen = new Map<string, string>()
  for (const a of addresses.value) if (a.business_id && !seen.has(a.business_id)) seen.set(a.business_id, a.business_name || a.business_id)
  return Array.from(seen, ([id, name]) => ({ id, name }))
})
const selected = computed(() => addresses.value.find(a => a.business_id === selectedBusinessId.value) || null)

// Current local date/time as a datetime-local string (YYYY-MM-DDTHH:mm).
function nowLocal(): string {
  const d = new Date()
  d.setMinutes(d.getMinutes() - d.getTimezoneOffset())
  return d.toISOString().slice(0, 16)
}

watch(() => props.open, async (isOpen) => {
  if (!isOpen) return
  selectedBusinessId.value = ''
  noteText.value = ''
  when.value = nowLocal()
  addresses.value = []
  accountId.value = ''
  loading.value = true
  try {
    const list = await accountsService.list()
    const accts = (list.data as any)?.data?.accounts || (list.data as any)?.accounts || []
    const amx = accts.find((a: any) => a.ameex_enabled && a.has_ameex_api_key) || accts.find((a: any) => a.ameex_enabled)
    if (!amx) {
      toast.error(t('accounts.pickupNoAccount', 'Aucun numéro Ameex activé'))
      emit('update:open', false)
      return
    }
    accountId.value = amx.id
    const res = await accountsService.ameexPickupAddresses(amx.id)
    addresses.value = (res.data as any)?.data?.addresses || (res.data as any)?.addresses || []
    if (businesses.value.length === 1) selectedBusinessId.value = businesses.value[0].id
  } catch (e) {
    toast.error(getErrorMessage(e, t('accounts.pickupAddrFailed', 'Impossible de charger les adresses de ramassage')))
  } finally {
    loading.value = false
  }
})

async function submit() {
  const addr = selected.value
  if (!accountId.value || !addr) {
    toast.error(t('accounts.pickupPickBiz', 'Choisissez un business'))
    return
  }
  isSubmitting.value = true
  try {
    const whenStr = when.value ? when.value.replace('T', ' ') : ''
    const note = [whenStr, noteText.value.trim()].filter(Boolean).join(' — ')
    const res = await accountsService.ameexPickup(accountId.value, {
      business: addr.business_id || undefined,
      city_id: Number(addr.city_id) || undefined,
      address: addr.address || undefined,
      phone: addr.phone || undefined,
      note: note || undefined,
    })
    const d = (res.data as any)?.data || res.data
    toast.success(d?.msg || t('accounts.pickupRequested', 'Ramassage demandé'))
    emit('update:open', false)
  } catch (e) {
    toast.error(getErrorMessage(e, t('accounts.pickupFailed', 'Échec de la demande de ramassage')))
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{{ $t('accounts.requestPickup', 'Demander un ramassage') }}</DialogTitle>
        <DialogDescription>{{ $t('accounts.pickupDesc', 'Ameex viendra récupérer les colis à cette adresse.') }}</DialogDescription>
      </DialogHeader>
      <div v-if="loading" class="py-6 flex justify-center">
        <Loader2 class="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
      <div v-else class="space-y-3 py-2">
        <div class="space-y-1.5">
          <Label class="text-xs">Business</Label>
          <select
            :value="selectedBusinessId"
            @change="selectedBusinessId = ($event.target as HTMLSelectElement).value"
            class="flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm"
          >
            <option value="" disabled>{{ $t('accounts.pickupPickBiz', 'Choisissez un business') }}</option>
            <option v-for="b in businesses" :key="b.id" :value="b.id">{{ b.name }}</option>
          </select>
          <p v-if="businesses.length === 0" class="text-[11px] text-muted-foreground">
            {{ $t('accounts.pickupNoAddr', 'Aucune adresse enregistrée dans Ameex. Ajoutez-en une dans le tableau de bord Ameex.') }}
          </p>
        </div>
        <!-- Address auto-fetched from Ameex for the chosen business -->
        <div v-if="selected" class="rounded-md bg-muted/50 px-3 py-2 text-[12px] space-y-0.5">
          <div><span class="text-muted-foreground">{{ $t('accounts.pickupCity', 'Ville') }}:</span> {{ selected.city_name || '—' }}</div>
          <div><span class="text-muted-foreground">{{ $t('accounts.pickupPhone', 'Téléphone') }}:</span> {{ selected.phone || '—' }}</div>
          <div><span class="text-muted-foreground">{{ $t('accounts.pickupAddress', 'Adresse') }}:</span> {{ selected.address || '—' }}</div>
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">{{ $t('accounts.pickupTime', 'Heure de ramassage') }}</Label>
          <input
            v-model="when"
            type="datetime-local"
            class="flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm"
          />
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">{{ $t('accounts.pickupNote', 'Note') }}</Label>
          <textarea
            v-model="noteText"
            rows="2"
            class="flex w-full rounded-md border border-input bg-background px-3 py-2 text-sm shadow-sm"
            :placeholder="$t('accounts.pickupNotePlaceholder', 'ex: 5 colis prêts')"
          ></textarea>
        </div>
      </div>
      <DialogFooter>
        <Button variant="outline" @click="emit('update:open', false)">{{ $t('common.cancel', 'Annuler') }}</Button>
        <Button :disabled="isSubmitting || loading" @click="submit">
          <Loader2 v-if="isSubmitting" class="h-4 w-4 mr-2 animate-spin" />
          {{ $t('accounts.requestPickup', 'Demander un ramassage') }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
