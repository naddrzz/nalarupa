Update(page, {fill: "#F5F5F5"});
Delete(Get(page, {depth: 1}).children[0].id);
Print(Get(page, (n, c) => [n.name, c.depth, c.bounds]));
