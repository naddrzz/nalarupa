// A failed call on the empty template saves nothing: the next call starts from the template again.
Insert(document, {type: "frame", name: "Gone", width: 10, height: 10});
throw new Error("first call fails");
