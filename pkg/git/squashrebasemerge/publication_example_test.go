package squashrebasemerge_test

import (
	"fmt"
	"github.com/colony-2/c2j/pkg/git/squashrebasemerge"
)

func ExamplePublicationDisposition() {
	output := squashrebasemerge.MergeOutput{CandidateSHA: "abc", CandidateSubject: "Actual subject", PublicationBranch: "refs/heads/main", PublicationDisposition: squashrebasemerge.PublicationAlreadyPresent}
	verified := output.PublicationDisposition == squashrebasemerge.PublicationPublished || output.PublicationDisposition == squashrebasemerge.PublicationAlreadyPresent
	fmt.Println(verified, output.CandidateSubject, output.PublicationBranch)
	// Output: true Actual subject refs/heads/main
}
