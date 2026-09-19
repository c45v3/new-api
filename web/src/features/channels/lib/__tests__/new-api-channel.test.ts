/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { describe, expect, test } from 'vitest'

import {
  CHANNEL_TYPE_NEW_API,
  CHANNEL_TYPE_OPTIONS,
  MODEL_FETCHABLE_TYPES,
} from '../../constants'
import type { Channel } from '../../types'
import {
  buildSettingJSON,
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
} from '../channel-form'
import { getChannelTypeConfig } from '../channel-type-config'
import { getChannelTypeIcon, getKeyPromptForType } from '../channel-utils'

function newAPIForm(baseUrl: string) {
  return {
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'New API upstream',
    type: CHANNEL_TYPE_NEW_API,
    base_url: baseUrl,
    key: 'test-key',
    models: 'gpt-5',
  }
}

describe('New API channel', () => {
  test('registers selection, ordering, model discovery, and icon metadata', () => {
    const option = CHANNEL_TYPE_OPTIONS.find(
      (item) => item.value === CHANNEL_TYPE_NEW_API
    )

    expect(option).toEqual({
      value: CHANNEL_TYPE_NEW_API,
      label: 'New API',
    })
    expect(
      CHANNEL_TYPE_OPTIONS.findIndex(
        (item) => item.value === CHANNEL_TYPE_NEW_API
      ) + 1
    ).toBe(CHANNEL_TYPE_OPTIONS.findIndex((item) => item.value === 58))
    expect(MODEL_FETCHABLE_TYPES.has(CHANNEL_TYPE_NEW_API)).toBe(true)
    expect(getChannelTypeIcon(CHANNEL_TYPE_NEW_API)).toBe('NewAPI')
    expect(getKeyPromptForType(CHANNEL_TYPE_NEW_API)).toBe(
      'Enter API key for this channel'
    )
    expect(getChannelTypeConfig(CHANNEL_TYPE_NEW_API).icon).toBe('NewAPI')
  })

  test('requires a non-blank Base URL', () => {
    const blankResult = channelFormSchema.safeParse(newAPIForm('  '))

    expect(blankResult.success).toBe(false)
    if (!blankResult.success) {
      expect(
        blankResult.error.issues.some(
          (issue) =>
            issue.path[0] === 'base_url' &&
            issue.message === 'Base URL is required for this channel type'
        )
      ).toBe(true)
    }

    expect(
      channelFormSchema.safeParse(newAPIForm('https://new-api.example')).success
    ).toBe(true)
  })

  test('keeps Sub2API Base URL validation unchanged', () => {
    const result = channelFormSchema.safeParse({
      ...newAPIForm(''),
      type: 59,
    })

    expect(result.success).toBe(true)
  })

  test('requires explicit external billing only for transparent transport', () => {
    const values = {
      ...newAPIForm('https://new-api.example'),
      transport_mode: 'transparent' as const,
    }
    const result = channelFormSchema.safeParse(values)
    expect(result.success).toBe(false)
    if (!result.success) {
      expect(
        result.error.issues.some(
          (issue) => issue.path[0] === 'transparent_billing'
        )
      ).toBe(true)
    }
    expect(
      channelFormSchema.safeParse({
        ...values,
        transparent_billing: 'external',
      }).success
    ).toBe(true)
    expect(
      channelFormSchema.safeParse({ ...values, transport_mode: 'convert' })
        .success
    ).toBe(true)
  })

  test('rejects request mutations for transparent transport but permits empty mappings', () => {
    const values = {
      ...newAPIForm('https://new-api.example'),
      transport_mode: 'transparent',
      transparent_billing: 'external',
      model_mapping: '{ }',
    }
    expect(channelFormSchema.safeParse(values).success).toBe(true)
    const result = channelFormSchema.safeParse({
      ...values,
      model_mapping: '{"gpt-5":"other"}',
    })
    expect(result.success).toBe(false)
    if (!result.success) {
      expect(
        result.error.issues.some((issue) => issue.path[0] === 'transport_mode')
      ).toBe(true)
    }
  })

  test('roundtrips transparent settings without losing legacy or unknown settings', () => {
    const channel = {
      channel_info: {
        is_multi_key: false,
        multi_key_size: 0,
        multi_key_polling_index: 0,
        multi_key_mode: 'random',
      },
      ...newAPIForm('https://new-api.example'),
      id: 1,
      group: 'default',
      setting: JSON.stringify({
        transport_mode: 'transparent',
        transparent_billing: 'external',
        pass_through_body_enabled: true,
        proxy: 'https://proxy.example',
        future_setting: { enabled: true },
      }),
    } as Channel
    const saved = JSON.parse(
      buildSettingJSON(transformChannelToFormDefaults(channel))
    )
    expect(saved).toMatchObject(JSON.parse(channel.setting ?? ''))
  })
})
