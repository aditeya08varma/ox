#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub enum EvictionOrder {
    #[default]
    LeastRecentlySelectedGeneration,
    ClockSecondChance,
    ApproxLeastFrequentlyUsed,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum Touch {
    None,
    SetRefBit,
    BumpFrequency,
}

impl EvictionOrder {
    pub(crate) const fn touch(self) -> Touch {
        match self {
            Self::LeastRecentlySelectedGeneration => Touch::None,
            Self::ClockSecondChance => Touch::SetRefBit,
            Self::ApproxLeastFrequentlyUsed => Touch::BumpFrequency,
        }
    }
}

pub(crate) struct AdmissionCandidate<'a, T> {
    pub key: &'a str,
    pub value: &'a T,
    pub size: u64,
}

pub(crate) struct AdmitEverything;

impl AdmitEverything {
    pub(crate) fn select<T: Clone>(
        capacity: u64,
        resident: &std::collections::BTreeSet<String>,
        candidates: &[AdmissionCandidate<'_, T>],
    ) -> Vec<T> {
        let mut seen = std::collections::BTreeSet::new();
        let mut selected = Vec::new();
        for candidate in candidates {
            if resident.contains(candidate.key) || !seen.insert(candidate.key) {
                continue;
            }
            if candidate.size > capacity {
                break;
            }
            selected.push(candidate.value.clone());
        }
        selected
    }
}
