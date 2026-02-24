package indexed

// Comparable for value-comparison on update function. I think it might be a reasonable requirement.
// I could add others for the Table2 style, and Map, and allow some global functions to require
// conforming record types.
type Record comparable
