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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, beforeAll, describe, expect, test } from 'vitest'

import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { DetailsDialog } from '../dialogs/details-dialog'
import { ModelBadge } from '../model-badge'
import { ReasoningEffortBadge } from '../reasoning-effort-badge'

const i18nKeys = {
  'Log Details': 'Log Details',
  Consume: 'Consume',
  'Reasoning Effort': 'Reasoning Effort',
}

function makeLog(other: LogOtherData): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    username: 'user',
    token_name: 'token',
    model_name: 'gpt-5',
    quota: 5000,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 0,
    is_stream: false,
    channel: 1,
    channel_name: '',
    token_id: 1,
    group: 'default',
    ip: '',
    other: JSON.stringify(other),
    request_id: 'req-1',
    upstream_request_id: '',
  }
}

function renderDetails(other: LogOtherData): QueryClient {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const freshAt = Date.now() + 60_000
  queryClient.setQueryData(['status'], {}, { updatedAt: freshAt })
  queryClient.setQueryData(
    ['pricing'],
    { data: [], vendors: [] },
    { updatedAt: freshAt }
  )

  render(
    <QueryClientProvider client={queryClient}>
      <DetailsDialog
        log={makeLog(other)}
        isAdmin={false}
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
  return queryClient
}

function ModelCellLike(props: {
  modelName: string
  effort: string | undefined
}) {
  return (
    <div className='flex w-fit flex-col gap-0.5'>
      <ModelBadge modelName={props.modelName} />
      <ReasoningEffortBadge effort={props.effort} compact />
    </div>
  )
}

describe('reasoning effort in usage logs', () => {
  const queryClients: QueryClient[] = []

  beforeAll(() => {
    i18next.addResourceBundle('en', 'translation', i18nKeys)
  })

  afterEach(() => {
    for (const queryClient of queryClients) {
      queryClient.clear()
    }
    queryClients.length = 0
  })

  test('renders the stored effort token and hides blank values', () => {
    const { rerender } = render(<ReasoningEffortBadge effort='high' />)
    expect(screen.getByText('high')).toBeInTheDocument()

    rerender(<ReasoningEffortBadge effort='  ' />)
    expect(screen.queryByText('high')).toBeNull()

    rerender(<ReasoningEffortBadge effort={undefined} />)
    expect(screen.queryByText('high')).toBeNull()
  })

  test('shows reasoning effort in the details overview', () => {
    queryClients.push(renderDetails({ reasoning_effort: 'high' }))

    expect(screen.getByText('Reasoning Effort')).toBeInTheDocument()
    expect(screen.getByText('high')).toBeInTheDocument()
  })

  test('hides reasoning effort when the field is absent', () => {
    queryClients.push(renderDetails({}))

    expect(screen.queryByText('Reasoning Effort')).toBeNull()
  })

  test('shows compact effort under the model name in the list cell', () => {
    const { rerender } = render(
      <ModelCellLike modelName='gpt-5' effort='low' />
    )
    expect(screen.getByText('gpt-5')).toBeInTheDocument()
    expect(screen.getByText('low')).toBeInTheDocument()

    rerender(<ModelCellLike modelName='gpt-5' effort={undefined} />)
    expect(screen.getByText('gpt-5')).toBeInTheDocument()
    expect(screen.queryByText('low')).toBeNull()
  })
})
