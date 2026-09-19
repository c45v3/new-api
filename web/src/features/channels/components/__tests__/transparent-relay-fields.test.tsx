import { zodResolver } from '@hookform/resolvers/zod'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { expect, test, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  createChannelFormSchema,
  type ChannelFormValues,
} from '../../lib/channel-form'
import { TransparentRelayFields } from '../transparent-relay-fields'

function Harness(props: {
  supported: readonly number[] | undefined
  initial?: Partial<ChannelFormValues>
  onSave: (values: ChannelFormValues) => void
}) {
  const form = useForm<ChannelFormValues>({
    resolver: zodResolver(createChannelFormSchema(props.supported)),
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'Relay channel',
      type: 1,
      key: 'upstream-key',
      models: 'gpt-5',
      ...props.initial,
    },
  })
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(props.onSave)}>
        <TransparentRelayFields
          form={form}
          supportedChannelTypes={props.supported}
        />
        <FormField
          control={form.control}
          name='disguise_as_claude_code'
          render={({ field }) => (
            <FormItem>
              <FormLabel>Disguise as Claude Code</FormLabel>
              <FormControl>
                <Switch
                  checked={field.value === true}
                  onCheckedChange={field.onChange}
                />
              </FormControl>
            </FormItem>
          )}
        />
        <Button type='submit'>Save</Button>
      </form>
    </Form>
  )
}

test('enabling Transparent Relay requires external billing before save', async () => {
  const onSave = vi.fn()
  const user = userEvent.setup()
  render(<Harness supported={[1]} onSave={onSave} />)
  expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
  expect(screen.queryByLabelText('Transport mode')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('switch', { name: 'Request Body Passthrough' })
  ).not.toBeInTheDocument()
  const toggle = screen.getByRole('switch', { name: 'Transparent Relay' })
  toggle.focus()
  await user.keyboard(' ')
  expect(toggle).toBeChecked()
  await user.click(screen.getByRole('button', { name: 'Save' }))
  expect(
    await screen.findByText('Confirm external billing for Transparent Relay')
  ).toBeVisible()
  expect(onSave).not.toHaveBeenCalled()
  await user.click(
    screen.getByRole('combobox', { name: 'Transparent Relay billing' })
  )
  await user.click(
    await screen.findByRole('option', { name: 'I use external billing' })
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({
        transparent_relay: true,
        transparent_billing: 'external',
      }),
      expect.anything()
    )
  )
  await user.click(toggle)
  expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
})

test('unavailable capabilities prevent enabling until the server supplies support', () => {
  const onSave = vi.fn()
  const view = render(<Harness supported={undefined} onSave={onSave} />)
  expect(
    screen.getByRole('switch', { name: 'Transparent Relay' })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByRole('status')).toHaveTextContent(
    'Transparent Relay capabilities are unavailable.'
  )
  view.rerender(<Harness supported={[1]} onSave={onSave} />)
  expect(
    screen.getByRole('switch', { name: 'Transparent Relay' })
  ).not.toHaveAttribute('aria-disabled', 'true')
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
})

test('a Custom endpoint channel cannot enable Transparent Relay', () => {
  render(
    <Harness
      supported={[1]}
      initial={{
        type: 8,
        base_url: 'https://upstream.example/v1/chat/completions',
      }}
      onSave={vi.fn()}
    />
  )
  expect(
    screen.getByRole('switch', { name: 'Transparent Relay' })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByRole('status')).toHaveTextContent(
    'Transparent Relay is not supported for this channel type.'
  )
})

test('Claude Code disguise visibly blocks transparent saving and allows conflict recovery', async () => {
  const onSave = vi.fn()
  const user = userEvent.setup()
  render(
    <Harness
      supported={[1]}
      initial={{ transparent_relay: true, transparent_billing: 'external' }}
      onSave={onSave}
    />
  )
  await user.click(
    screen.getByRole('switch', { name: 'Disguise as Claude Code' })
  )
  expect(screen.getByRole('status')).toHaveTextContent(
    'Transparent Relay cannot be used together with Claude Code disguise'
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(
      screen.getByRole('switch', { name: 'Transparent Relay' })
    ).toHaveAttribute('aria-invalid', 'true')
  )
  expect(onSave).not.toHaveBeenCalled()
  await user.click(screen.getByRole('switch', { name: 'Transparent Relay' }))
  expect(
    screen.getByRole('switch', { name: 'Transparent Relay' })
  ).toHaveAttribute('aria-disabled', 'true')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({
        transparent_relay: false,
        disguise_as_claude_code: true,
      }),
      expect.anything()
    )
  )
})

test('capability loss rejects a previously enabled channel but permits turning it off', async () => {
  const onSave = vi.fn()
  const user = userEvent.setup()
  const initial = {
    transparent_relay: true,
    transparent_billing: 'external' as const,
  }
  const view = render(
    <Harness supported={[1]} initial={initial} onSave={onSave} />
  )
  view.rerender(
    <Harness supported={undefined} initial={initial} onSave={onSave} />
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(
      screen.getByRole('switch', { name: 'Transparent Relay' })
    ).toHaveAttribute('aria-invalid', 'true')
  )
  expect(onSave).not.toHaveBeenCalled()
  await user.click(screen.getByRole('switch', { name: 'Transparent Relay' }))
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({ transparent_relay: false }),
      expect.anything()
    )
  )
})
