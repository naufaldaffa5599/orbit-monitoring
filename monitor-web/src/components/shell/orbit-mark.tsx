import geometry from "@/components/shell/orbit-geometry.json"

/**
 * The Orbit mark: a filled centre with a tilted ring around it.
 *
 * Two shapes and nothing else, because this has to survive being drawn at 16px
 * in a browser tab. The numbers live in orbit-geometry.json so that this
 * component, the favicon, and the PWA icons cannot drift apart — edit them
 * there and run `npm run icons`.
 *
 * Colour comes from `currentColor`, so the mark inherits the theme rather than
 * hardcoding the brand orange.
 */
export function OrbitMark({ className }: { className?: string }) {
  const { viewBox, ellipse, dot } = geometry
  return (
    <svg
      viewBox={`0 0 ${viewBox} ${viewBox}`}
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      className={className}
      aria-hidden="true"
    >
      {/* The orbit. Tilted so it reads as a ring seen edge-on rather than as
          a circle with a dot in it. */}
      <ellipse
        cx={ellipse.cx}
        cy={ellipse.cy}
        rx={ellipse.rx}
        ry={ellipse.ry}
        stroke="currentColor"
        strokeWidth={ellipse.strokeWidth}
        transform={`rotate(${ellipse.rotate} ${ellipse.cx} ${ellipse.cy})`}
      />
      {/* The thing being orbited. */}
      <circle cx={dot.cx} cy={dot.cy} r={dot.r} fill="currentColor" />
    </svg>
  )
}
