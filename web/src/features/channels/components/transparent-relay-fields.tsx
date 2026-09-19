import { useWatch, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import type { ChannelFormValues } from '../lib/channel-form'

interface TransparentRelayFieldsProps {
  form: UseFormReturn<ChannelFormValues>
  supportedChannelTypes: readonly number[] | undefined
}

export function TransparentRelayFields(props: TransparentRelayFieldsProps) {
  const { t } = useTranslation()
  const [type, enabled, disguise] = useWatch({
    control: props.form.control,
    name: ['type', 'transparent_relay', 'disguise_as_claude_code'],
  })
  const supported = props.supportedChannelTypes?.includes(type) === true

  return (
    <div className='space-y-4'>
      <FormField
        control={props.form.control}
        name='transparent_relay'
        render={({ field }) => (
          <FormItem>
            <div className='flex items-center justify-between gap-4'>
              <FormLabel>{t('Transparent Relay')}</FormLabel>
              <FormControl>
                <Switch
                  checked={field.value === true}
                  onCheckedChange={field.onChange}
                  disabled={!field.value && (!supported || disguise === true)}
                />
              </FormControl>
            </div>
            <FormDescription>
              {t(
                'Forward HTTP requests and responses as-is where possible. New API retains authentication, channel selection, upstream credential replacement, and basic rate limiting. No local usage parsing, billing, or retries.'
              )}
            </FormDescription>
            {!supported && (
              <p className='text-destructive text-sm' role='status'>
                {props.supportedChannelTypes
                  ? t(
                      'Transparent Relay is not supported for this channel type.'
                    )
                  : t(
                      'Transparent Relay capabilities are unavailable. Try again before enabling it.'
                    )}
              </p>
            )}
            {disguise && (
              <p className='text-destructive text-sm' role='status'>
                {t(
                  'Transparent Relay cannot be used together with Claude Code disguise because Claude Code disguise modifies the request.'
                )}
              </p>
            )}
            <FormMessage />
          </FormItem>
        )}
      />
      {enabled && (
        <FormField
          control={props.form.control}
          name='transparent_billing'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Transparent Relay billing')}</FormLabel>
              <Select
                items={[
                  { value: '', label: t('Select billing confirmation') },
                  { value: 'external', label: t('I use external billing') },
                ]}
                value={field.value || ''}
                onValueChange={field.onChange}
              >
                <FormControl>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                </FormControl>
                <SelectContent>
                  <SelectItem value=''>
                    {t('Select billing confirmation')}
                  </SelectItem>
                  <SelectItem value='external'>
                    {t('I use external billing')}
                  </SelectItem>
                </SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )}
        />
      )}
    </div>
  )
}
