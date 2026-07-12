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
import { useState, useEffect } from 'react'

interface UseTypingEffectOptions {
  texts: string[]
  typeSpeed?: number
  deleteSpeed?: number
  holdDuration?: number
  startDelay?: number
}

type Phase = 'typing' | 'holding' | 'deleting'

export function useTypingEffect(options: UseTypingEffectOptions): string {
  const {
    texts,
    typeSpeed = 60,
    deleteSpeed = 30,
    holdDuration = 1800,
    startDelay = 300,
  } = options
  const [displayedText, setDisplayedText] = useState('')

  const key = texts.join('\u0000')

  useEffect(() => {
    const phrases = key.split('\u0000')
    const prefersReducedMotion = window.matchMedia(
      '(prefers-reduced-motion: reduce)'
    ).matches

    // Show the first phrase in full when motion is reduced.
    if (prefersReducedMotion) {
      setDisplayedText(phrases[0] ?? '')
      return
    }

    setDisplayedText('')

    let timer: ReturnType<typeof setTimeout> | undefined
    let phraseIndex = 0
    let charIndex = 0
    let phase: Phase = 'typing'

    const tick = () => {
      const current = phrases[phraseIndex] ?? ''

      if (phase === 'typing') {
        charIndex++
        setDisplayedText(current.slice(0, charIndex))
        if (charIndex >= current.length) {
          phase = 'holding'
          timer = setTimeout(tick, holdDuration)
          return
        }
        timer = setTimeout(tick, typeSpeed)
        return
      }

      if (phase === 'holding') {
        phase = 'deleting'
        timer = setTimeout(tick, deleteSpeed)
        return
      }

      // deleting
      charIndex--
      setDisplayedText(current.slice(0, Math.max(charIndex, 0)))
      if (charIndex <= 0) {
        phase = 'typing'
        phraseIndex = (phraseIndex + 1) % phrases.length
        timer = setTimeout(tick, typeSpeed)
        return
      }
      timer = setTimeout(tick, deleteSpeed)
    }

    timer = setTimeout(tick, startDelay)

    return () => {
      if (timer) clearTimeout(timer)
    }
  }, [key, typeSpeed, deleteSpeed, holdDuration, startDelay])

  return displayedText
}
