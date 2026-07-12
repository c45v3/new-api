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
import { useEffect, useRef } from 'react'

const TAU = Math.PI * 2

export function EclipseHalo() {
  const canvasRef = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return

    const context = canvas.getContext('2d')
    if (!context) return

    const media = window.matchMedia('(prefers-reduced-motion: reduce)')
    let animationFrame = 0
    let glowRotation = 0
    let previousTime = performance.now()

    const drawArc = (
      radius: number,
      width: number,
      blur: number,
      alpha: number,
      offsetX = 0,
      hue: 'white' | 'red' | 'blue' = 'white',
      rotation = 0
    ) => {
      context.save()
      context.rotate(rotation)
      context.translate(offsetX, 0)
      context.lineWidth = width
      context.lineCap = 'round'
      context.filter = `blur(${blur}px)`

      const gradient = context.createConicGradient(-Math.PI * 0.82, 0, 0)
      let color = '255,255,255'
      if (hue === 'red') color = '255,55,90'
      if (hue === 'blue') color = '80,125,255'
      gradient.addColorStop(0, `rgba(${color},${alpha})`)
      gradient.addColorStop(0.055, `rgba(${color},${alpha * 0.86})`)
      gradient.addColorStop(0.11, `rgba(${color},${alpha * 0.48})`)
      gradient.addColorStop(0.17, `rgba(${color},0)`)
      gradient.addColorStop(0.83, `rgba(${color},0)`)
      gradient.addColorStop(0.89, `rgba(${color},${alpha * 0.48})`)
      gradient.addColorStop(0.945, `rgba(${color},${alpha * 0.86})`)
      gradient.addColorStop(1, `rgba(${color},${alpha})`)
      context.strokeStyle = gradient
      context.beginPath()
      context.arc(0, 0, radius, 0, TAU)
      context.stroke()
      context.restore()
    }

    const render = () => {
      const rect = canvas.getBoundingClientRect()
      const dpr = Math.min(window.devicePixelRatio || 1, 2)
      const pixelWidth = Math.round(rect.width * dpr)
      const pixelHeight = Math.round(rect.height * dpr)

      if (canvas.width !== pixelWidth || canvas.height !== pixelHeight) {
        canvas.width = pixelWidth
        canvas.height = pixelHeight
      }

      context.setTransform(dpr, 0, 0, dpr, 0, 0)
      context.clearRect(0, 0, rect.width, rect.height)
      context.save()
      context.globalCompositeOperation = 'screen'
      context.translate(rect.width / 2, rect.height * 0.34)

      const diameter = Math.min(Math.max(rect.width * 0.42, 260), 420)
      const radius = Math.min(diameter / 2, rect.height * 0.32)
      const outerGlowRotation = media.matches ? 0 : glowRotation

      // One coherent moving light source: every corona layer shares one phase.
      drawArc(radius, 48, 72, 0.271, 0, 'white', outerGlowRotation)
      drawArc(radius, 20, 28, 0.561, 0, 'white', outerGlowRotation)
      drawArc(radius, 9, 8, 0.889, 0, 'white', outerGlowRotation)
      drawArc(radius, 3, 2.5, 1, 0, 'white', outerGlowRotation)

      // Linear inner mask makes the radial glow a symmetric triangular profile:
      // brightest at the rim, fading evenly toward the dark core.
      const moon = context.createRadialGradient(
        0,
        0,
        radius * 0.68,
        0,
        0,
        radius
      )
      moon.addColorStop(0, 'rgba(0,0,0,1)')
      moon.addColorStop(0.25, 'rgba(0,0,0,0.75)')
      moon.addColorStop(0.5, 'rgba(0,0,0,0.5)')
      moon.addColorStop(0.75, 'rgba(0,0,0,0.25)')
      moon.addColorStop(1, 'rgba(0,0,0,0)')
      context.globalCompositeOperation = 'source-over'
      context.fillStyle = moon
      context.beginPath()
      context.arc(0, 0, radius, 0, TAU)
      context.fill()

      // Screen blending keeps the rim dark at rest and lets passing light brighten it.
      context.globalCompositeOperation = 'screen'
      context.filter = 'none'
      context.lineWidth = 3.5
      context.strokeStyle = '#191919'
      context.beginPath()
      context.arc(0, 0, radius, 0, TAU)
      context.stroke()

      context.restore()
    }

    const animate = (time: number) => {
      const delta = Math.min(time - previousTime, 100)
      previousTime = time
      glowRotation += delta * (TAU / 18000)
      render()
      animationFrame = requestAnimationFrame(animate)
    }

    const syncMotion = () => {
      cancelAnimationFrame(animationFrame)
      previousTime = performance.now()
      if (media.matches) {
        glowRotation = 0
        render()
      } else {
        animationFrame = requestAnimationFrame(animate)
      }
    }

    const resizeObserver = new ResizeObserver(render)
    resizeObserver.observe(canvas)
    media.addEventListener('change', syncMotion)
    syncMotion()

    return () => {
      cancelAnimationFrame(animationFrame)
      resizeObserver.disconnect()
      media.removeEventListener('change', syncMotion)
    }
  }, [])

  return (
    <canvas
      ref={canvasRef}
      aria-hidden
      className='pointer-events-none absolute inset-x-0 top-0 z-0 h-svh w-full'
    />
  )
}
