package model

// shareWallClock fills the allocation of a compound command from its wall clock. A literal
// share (sleep N) keeps its recorded duration, capped only for this derivation when the live
// operation has not run that long yet; the immutable evidence stays in literalMs for the next
// refresh. The remainder is divided equally among the other categories, with the last taking
// the rounding remainder so the shares sum to the wall clock exactly.
func shareWallClock(o *Operation, now int64) {
	if len(o.Shares) == 0 {
		return
	}
	end := o.End
	if o.Open && now > end {
		end = now
	}
	left := max(end-o.Start, 1)
	equal := 0
	for i := range o.Shares {
		sh := &o.Shares[i]
		if !sh.Literal {
			equal++
			continue
		}
		if sh.literalMs == 0 {
			sh.literalMs = sh.Ms
		}
		sh.Ms = min(sh.literalMs, left)
		left -= sh.Ms
	}
	if equal == 0 {
		o.Shares[len(o.Shares)-1].Ms += left
		return
	}
	each := left / int64(equal)
	given := int64(0)
	last := -1
	for i := range o.Shares {
		if !o.Shares[i].Literal {
			o.Shares[i].Ms = each
			given += each
			last = i
		}
	}
	o.Shares[last].Ms += left - given
}
