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
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { useTypingEffect } from '../../hooks/use-typing-effect'

interface HeroProps {
  className?: string
  isAuthenticated?: boolean
}

export function Hero(props: HeroProps) {
  const { t } = useTranslation()

  const typedText = useTypingEffect({
    texts: [
      t('Build Your Dream by Natural Language'),
      t('Seeking the Optimal Conversion from Energy to Intelligence'),
    ],
    typeSpeed: 60,
    deleteSpeed: 30,
    holdDuration: 1800,
    startDelay: 300,
  })

  return (
    <section className='relative z-10 overflow-hidden px-6 pt-48 pb-16 md:pt-56'>
      <div className='mx-auto w-full max-w-2xl text-center'>
        {/* 主标题 - 固定高度防止打字时抖动 */}
        <div className='flex min-h-[8rem] items-center justify-center'>
          <h1
            className='text-[clamp(1.75rem,4vw,3rem)] leading-[1.1] tracking-tight'
            style={{ fontWeight: 305 }}
          >
            <span
              className='bg-clip-text text-transparent'
              style={{
                backgroundImage:
                  'linear-gradient(110deg, oklch(0.88 0.045 205), oklch(0.93 0.02 240), oklch(0.84 0.045 275), oklch(0.93 0.02 240), oklch(0.88 0.045 205))',
                backgroundSize: '200% 100%',
                animation: 'hero-title-flow 8s linear infinite',
              }}
            >
              {typedText}
              <span
                aria-hidden
                className='ml-0.5 inline-block h-[0.7em] w-px bg-white align-baseline'
                style={{ animation: 'hero-cursor-blink 1s steps(1) infinite' }}
              />
            </span>
            <style>{`
              @keyframes hero-cursor-blink { 0%, 49% { opacity: 1 } 50%, 100% { opacity: 0 } }
              @keyframes hero-title-flow { 0% { background-position: 0% 50% } 100% { background-position: 200% 50% } }
              @media (prefers-reduced-motion: reduce) {
                [style*='hero-title-flow'] { animation: none !important }
              }
            `}</style>
          </h1>
        </div>

        {/* 按钮 */}
        {props.isAuthenticated && (
          <div className='mt-6 flex justify-center'>
            <Button
              size='sm'
              className='group rounded-lg border border-white/15 bg-white/10 px-4 text-sm font-medium text-white backdrop-blur-md hover:border-white/30 hover:bg-white/15'
              render={<Link to='/dashboard' />}
            >
              {t('Go to Dashboard')}
              <ArrowRight className='ml-1.5 size-3.5 transition-transform duration-200 group-hover:translate-x-1' />
            </Button>
          </div>
        )}
      </div>
    </section>
  )
}
