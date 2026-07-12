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
import { Link } from '@tanstack/react-router'
import { Terminal, TrendingUp, Tag } from 'lucide-react'

import { AnimateInView } from '@/components/animate-in-view'

interface FeaturesProps {
  className?: string
}

// Spotlight follows the pointer across the card via CSS custom properties.
const trackSpotlight = (event: React.MouseEvent<HTMLAnchorElement>) => {
  const rect = event.currentTarget.getBoundingClientRect()
  event.currentTarget.style.setProperty(
    '--spot-x',
    `${event.clientX - rect.left}px`
  )
  event.currentTarget.style.setProperty(
    '--spot-y',
    `${event.clientY - rect.top}px`
  )
}

export function Features(_props: FeaturesProps) {
  const features = [
    {
      id: 'console',
      title: 'Console',
      icon: <Terminal className='size-6' strokeWidth={1.5} />,
      link: '/dashboard',
    },
    {
      id: 'ranking',
      title: 'Ranking',
      icon: <TrendingUp className='size-6' strokeWidth={1.5} />,
      link: '/rankings',
    },
    {
      id: 'pricing',
      title: 'Pricing',
      icon: <Tag className='size-6' strokeWidth={1.5} />,
      link: '/pricing',
    },
  ]

  return (
    <section className='relative z-10 px-6 py-16 md:py-24'>
      <div className='mx-auto max-w-3xl space-y-6'>
        {features.map((feature, i) => (
          <AnimateInView key={feature.id} delay={i * 150} animation='fade-up'>
            <Link
              to={feature.link}
              onMouseMove={trackSpotlight}
              className='group relative block overflow-hidden rounded-xl border border-white/10 bg-white/5 px-6 py-4 backdrop-blur-md transition-all duration-300 hover:scale-[1.01] hover:border-white/25 hover:bg-white/[0.07] hover:shadow-[0_0_24px_-4px_oklch(0.7_0.2_280_/_35%)]'
            >
              {/* Pointer-tracking spotlight, revealed on hover */}
              <div
                aria-hidden
                className='pointer-events-none absolute inset-0 opacity-0 transition-opacity duration-300 group-hover:opacity-100'
                style={{
                  background:
                    'radial-gradient(220px circle at var(--spot-x, 50%) var(--spot-y, 50%), oklch(0.72 0.22 285 / 22%), transparent 70%)',
                }}
              />
              <div className='relative flex items-center gap-3'>
                <div className='flex size-10 shrink-0 items-center justify-center rounded-lg border border-white/10 bg-white/5 text-white/90 transition-all group-hover:scale-110 group-hover:border-white/20 group-hover:text-white'>
                  {feature.icon}
                </div>
                <h3 className='text-base font-medium text-white/90'>
                  {feature.title}
                </h3>
              </div>
            </Link>
          </AnimateInView>
        ))}
      </div>
    </section>
  )
}
