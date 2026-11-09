import React, { useId } from 'react';

/**
 * Seigaiha, the "blue ocean waves" pattern: rows of concentric arcs, each row
 * overlapping the one above it.
 */
export default function Waves({ className }) {
  const id = useId().replace(/:/g, '');
  const rings = [40, 32, 24, 16, 8];
  const centers = [
    [0, 0],
    [80, 0],
    [40, 20],
    [0, 40],
    [80, 40],
  ];
  return (
    <svg className={className} aria-hidden="true" focusable="false" preserveAspectRatio="none">
      <defs>
        <pattern id={`seigaiha-${id}`} width="80" height="40" patternUnits="userSpaceOnUse">
          {centers.map(([cx, cy]) => (
            <g key={`${cx}-${cy}`}>
              {rings.map((r) => (
                <circle key={r} cx={cx} cy={cy} r={r} className="wave-ring" />
              ))}
            </g>
          ))}
        </pattern>
      </defs>
      <rect width="100%" height="100%" fill={`url(#seigaiha-${id})`} />
    </svg>
  );
}
