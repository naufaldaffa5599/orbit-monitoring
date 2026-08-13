import geometry from "@/components/shell/orbit-geometry.json"

/**
 * The node marks: one family of shapes drawn from the same numbers as the
 * Orbit logo, so a row in the tree and the mark in the topbar read as the
 * same system.
 *
 * The shapes live as data in orbit-geometry.json rather than as JSX here,
 * because monitor-app has to draw the identical set without React. Both sides
 * render primitives out of that file instead of each keeping their own copy.
 *
 * A tree row draws these at about 14px, which is what the family is designed
 * around: the marks differ by which way the ring tilts, whether it is broken,
 * and whether a dot sits outside it. Anything subtler than that survives the
 * contact sheet and then disappears in the sidebar.
 */

const icons: Record<string, IconShapes> = geometry.icons

type IconShapes = {
  rings?: { rx: number; ry: number; rotate: number; strokeWidth: number; dash?: string }[]
  dots?: { cx: number; cy: number; r: number }[]
  squares?: { x: number; y: number; size: number; rx: number }[]
}

/** Whether a stored icon string names one of these marks rather than an emoji. */
export function isOrbitIcon(name: string | undefined): name is string {
  return !!name && name in icons
}

export function OrbitIcon({ name, className }: { name: string; className?: string }) {
  const shape = icons[name]
  if (!shape) return null
  const { viewBox } = geometry
  const c = viewBox / 2
  return (
    <svg
      viewBox={`0 0 ${viewBox} ${viewBox}`}
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      className={className}
      aria-hidden="true"
    >
      {shape.rings?.map((ring, i) => (
        <ellipse
          key={`r${i}`}
          cx={c}
          cy={c}
          rx={ring.rx}
          ry={ring.ry}
          stroke="currentColor"
          strokeWidth={ring.strokeWidth}
          strokeDasharray={ring.dash}
          strokeLinecap={ring.dash ? "round" : undefined}
          transform={`rotate(${ring.rotate} ${c} ${c})`}
        />
      ))}
      {shape.squares?.map((sq, i) => (
        <rect
          key={`s${i}`}
          x={sq.x}
          y={sq.y}
          width={sq.size}
          height={sq.size}
          rx={sq.rx}
          fill="currentColor"
        />
      ))}
      {shape.dots?.map((dot, i) => (
        <circle key={`d${i}`} cx={dot.cx} cy={dot.cy} r={dot.r} fill="currentColor" />
      ))}
    </svg>
  )
}
